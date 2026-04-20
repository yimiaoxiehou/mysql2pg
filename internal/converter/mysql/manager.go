package mysqlconverter

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yourusername/mysql2pg/internal/config"
	mysqlconn "github.com/yourusername/mysql2pg/internal/mysql"
)

// TableDataInconsistency 表数据不一致信息。
type TableDataInconsistency struct {
	TableName      string
	SourceRowCount int64
	TargetRowCount int64
}

// Manager 管理 MySQL 到 MySQL 的同步流程。
type Manager struct {
	sourceConn *mysqlconn.Connection
	targetConn *mysqlconn.Connection
	config     *config.Config

	errorLogFile *os.File
	logFile      *os.File

	totalTasks         int
	completedTasks     int
	mutex              sync.Mutex
	inconsistentTables []TableDataInconsistency
}

// NewManager 创建 MySQL 到 MySQL 的转换管理器实例。
func NewManager(sourceConn *mysqlconn.Connection, targetConn *mysqlconn.Connection, cfg *config.Config) (*Manager, error) {
	errorLogFile, err := os.OpenFile(cfg.Run.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("打开错误日志文件失败: %w", err)
	}

	var logFile *os.File
	if cfg.Run.EnableFileLogging && cfg.Run.LogFilePath != "" {
		logFile, err = os.OpenFile(cfg.Run.LogFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
	}

	return &Manager{
		sourceConn:   sourceConn,
		targetConn:   targetConn,
		config:       cfg,
		errorLogFile: errorLogFile,
		logFile:      logFile,
	}, nil
}

// Close 关闭管理器持有的日志资源。
func (m *Manager) Close() error {
	var err error
	if m.logFile != nil {
		if closeErr := m.logFile.Close(); closeErr != nil {
			err = closeErr
		}
	}
	if closeErr := m.errorLogFile.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

// Run 执行 MySQL 到 MySQL 的同步任务。
func (m *Manager) Run() error {
	if m.config.Conversion.Options.Compare {
		return m.runComparison()
	}

	if m.config.Conversion.Options.Functions ||
		m.config.Conversion.Options.Indexes ||
		m.config.Conversion.Options.Users ||
		m.config.Conversion.Options.View ||
		m.config.Conversion.Options.Grant ||
		m.config.Conversion.Options.TablePrivileges {
		m.Log("mysql2mysql 当前仅支持 tableddl/data，其他对象将跳过")
	}

	tables, err := m.sourceConn.GetTables(
		m.config.Conversion.Options.SkipUseTableList,
		m.config.Conversion.Options.SkipTableList,
		m.config.Conversion.Options.UseTableList,
		m.config.Conversion.Options.TableList,
	)
	if err != nil {
		return fmt.Errorf("获取源表信息失败: %w", err)
	}

	if len(tables) == 0 {
		m.Log("没有可同步的表，任务结束")
		return nil
	}

	m.totalTasks = 0
	if m.config.Conversion.Options.TableDDL {
		m.totalTasks += len(tables)
	}
	if m.config.Conversion.Options.Data {
		m.totalTasks += len(tables)
	}

	if m.config.Conversion.Options.TableDDL {
		if err := m.syncDDL(tables); err != nil {
			return err
		}
	}
	if m.config.Conversion.Options.Data {
		if err := m.syncData(tables); err != nil {
			return err
		}
	}

	m.displayInconsistentTables()
	m.Log("mysql2mysql 同步完成")
	return nil
}

// runComparison 执行 MySQL 到 MySQL 的数据对比流程。
func (m *Manager) runComparison() error {
	m.Log("开始执行 mysql2mysql 数据对比流程")
	m.inconsistentTables = nil
	m.completedTasks = 0

	tables, err := m.sourceConn.GetTables(
		m.config.Conversion.Options.SkipUseTableList,
		m.config.Conversion.Options.SkipTableList,
		m.config.Conversion.Options.UseTableList,
		m.config.Conversion.Options.TableList,
	)
	if err != nil {
		return fmt.Errorf("获取源表信息失败: %w", err)
	}
	if len(tables) == 0 {
		m.Log("未发现需要对比的表，流程结束")
		return nil
	}

	m.totalTasks = len(tables)
	if err := m.compareTableData(tables); err != nil {
		return err
	}
	m.displayInconsistentTables()

	if m.config.Run.CompareResultPath != "" {
		if err := m.exportInconsistentTablesToCSV(); err != nil {
			return err
		}
	}
	m.Log("mysql2mysql 数据对比流程完成")
	return nil
}

// compareTableData 对比表数据行数。
func (m *Manager) compareTableData(tables []mysqlconn.TableInfo) error {
	for _, table := range tables {
		sourceCount, err := m.sourceConn.GetTableRowCount(table.Name)
		if err != nil {
			return fmt.Errorf("获取源表 %s 行数失败: %w", table.Name, err)
		}

		targetCount, err := m.targetConn.GetTableRowCount(table.Name)
		if err != nil {
			// 目标表不存在时记录为 -1，其它错误直接返回。
			if strings.Contains(strings.ToLower(err.Error()), "doesn't exist") ||
				strings.Contains(strings.ToLower(err.Error()), "does not exist") {
				m.inconsistentTables = append(m.inconsistentTables, TableDataInconsistency{
					TableName:      table.Name,
					SourceRowCount: sourceCount,
					TargetRowCount: -1,
				})
			} else {
				return fmt.Errorf("获取目标表 %s 行数失败: %w", table.Name, err)
			}
		} else if sourceCount != targetCount {
			m.inconsistentTables = append(m.inconsistentTables, TableDataInconsistency{
				TableName:      table.Name,
				SourceRowCount: sourceCount,
				TargetRowCount: targetCount,
			})
		}

		m.updateProgress()
	}
	return nil
}

// exportInconsistentTablesToCSV 导出不一致表到 CSV。
func (m *Manager) exportInconsistentTablesToCSV() error {
	file, err := os.Create(m.config.Run.CompareResultPath)
	if err != nil {
		return fmt.Errorf("创建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	header := []string{"TableName", "SourceRowCount", "TargetRowCount", "Diff"}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("写入 CSV 表头失败: %w", err)
	}

	for _, table := range m.inconsistentTables {
		targetCountStr := strconv.FormatInt(table.TargetRowCount, 10)
		diffStr := strconv.FormatInt(table.SourceRowCount-table.TargetRowCount, 10)
		if table.TargetRowCount == -1 {
			targetCountStr = "不存在"
			diffStr = "N/A"
		}
		row := []string{
			table.TableName,
			strconv.FormatInt(table.SourceRowCount, 10),
			targetCountStr,
			diffStr,
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("写入 CSV 行失败: %w", err)
		}
	}

	m.Log("对比结果已导出到: %s", m.config.Run.CompareResultPath)
	return nil
}

// UpdateTableListFromCSV 从对比结果 CSV 解析差异表并更新同步表列表。
func (m *Manager) UpdateTableListFromCSV() error {
	filePath := m.config.Run.CompareResultPath
	if filePath == "" {
		return fmt.Errorf("未配置对比结果 CSV 路径 (compare_result_path)")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("打开对比结果 CSV 失败: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	if _, err := reader.Read(); err != nil {
		return fmt.Errorf("读取 CSV 表头失败: %w", err)
	}

	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("读取 CSV 内容失败: %w", err)
	}

	var diffTables []string
	for _, record := range records {
		if len(record) < 4 {
			continue
		}
		tableName := strings.TrimSpace(record[0])
		targetCount := strings.TrimSpace(record[2])
		diff := strings.TrimSpace(record[3])
		if tableName == "" {
			continue
		}
		if targetCount == "不存在" || diff != "0" {
			diffTables = append(diffTables, tableName)
		}
	}

	if len(diffTables) == 0 {
		m.Log("CSV 中未发现差异表，无需同步")
		return nil
	}

	m.config.Conversion.Options.UseTableList = true
	m.config.Conversion.Options.TableList = diffTables
	m.config.Conversion.Options.TableDDL = true
	m.config.Conversion.Options.Data = true

	m.Log("从 CSV 解析出 %d 个差异表，将仅同步这些表", len(diffTables))
	return nil
}

// syncDDL 同步所有表的建表语句。
func (m *Manager) syncDDL(tables []mysqlconn.TableInfo) error {
	for _, table := range tables {
		ddl, err := m.sourceConn.GetTableDDL(table.Name)
		if err != nil {
			return fmt.Errorf("获取表 %s DDL 失败: %w", table.Name, err)
		}

		exists, err := m.targetConn.TableExists(table.Name)
		if err != nil {
			return fmt.Errorf("检查目标表 %s 失败: %w", table.Name, err)
		}

		if exists {
			if m.config.Conversion.Options.SkipExistingTables {
				m.Log("表 %s 已存在，按配置跳过建表", table.Name)
				m.updateProgress()
				continue
			}
			dropSQL := fmt.Sprintf("DROP TABLE IF EXISTS `%s`", table.Name)
			if err := m.targetConn.ExecuteDDL(dropSQL); err != nil {
				return fmt.Errorf("删除目标表 %s 失败: %w", table.Name, err)
			}
		}

		if err := m.targetConn.ExecuteDDL(ddl); err != nil {
			return fmt.Errorf("创建目标表 %s 失败: %w", table.Name, err)
		}
		m.Log("同步表结构完成: %s", table.Name)
		m.updateProgress()
	}
	return nil
}

// syncData 同步所有表的数据内容。
func (m *Manager) syncData(tables []mysqlconn.TableInfo) error {
	for _, table := range tables {
		if err := m.syncSingleTableData(table.Name); err != nil {
			return err
		}
		m.updateProgress()
	}
	return nil
}

// syncSingleTableData 同步单表数据并执行行数校验。
func (m *Manager) syncSingleTableData(tableName string) error {
	columns, _, err := m.sourceConn.GetTableColumnsWithTypes(tableName)
	if err != nil {
		return fmt.Errorf("获取表 %s 列信息失败: %w", tableName, err)
	}

	if m.config.Conversion.Options.TruncateBeforeSync {
		truncateSQL := fmt.Sprintf("TRUNCATE TABLE `%s`", tableName)
		if err := m.targetConn.ExecuteDDL(truncateSQL); err != nil {
			return fmt.Errorf("清空目标表 %s 失败: %w", tableName, err)
		}
	}

	totalRows, err := m.sourceConn.GetTableRowCount(tableName)
	if err != nil {
		return fmt.Errorf("获取源表 %s 行数失败: %w", tableName, err)
	}

	batchSize := m.config.Conversion.Limits.MaxRowsPerBatch
	if batchSize <= 0 {
		batchSize = 1000
	}

	var processed int64
	for {
		rows, err := m.sourceConn.GetTableData(tableName, columns, int(processed), batchSize, "")
		if err != nil {
			return fmt.Errorf("读取源表 %s 数据失败: %w", tableName, err)
		}

		inserted, insertErr := m.insertRows(tableName, columns, rows)
		closeErr := rows.Close()
		if closeErr != nil {
			return fmt.Errorf("关闭源表 %s 游标失败: %w", tableName, closeErr)
		}
		if insertErr != nil {
			return insertErr
		}

		if inserted == 0 {
			break
		}
		processed += int64(inserted)
	}

	if m.config.Conversion.Options.ValidateData {
		targetRows, err := m.targetConn.GetTableRowCount(tableName)
		if err != nil {
			return fmt.Errorf("获取目标表 %s 行数失败: %w", tableName, err)
		}
		if targetRows != totalRows {
			m.mutex.Lock()
			m.inconsistentTables = append(m.inconsistentTables, TableDataInconsistency{
				TableName:      tableName,
				SourceRowCount: totalRows,
				TargetRowCount: targetRows,
			})
			m.mutex.Unlock()
		}
	}

	m.Log("同步表数据完成: %s (源行数: %d, 已处理: %d)", tableName, totalRows, processed)
	return nil
}

// insertRows 将查询结果批量写入目标 MySQL 表。
func (m *Manager) insertRows(tableName string, columns []string, rows *sql.Rows) (int, error) {
	insertSQL := buildInsertSQL(tableName, columns)
	insertCount := 0

	if m.config.Conversion.Options.UseTransaction {
		tx, err := m.targetConn.GetDB().Begin()
		if err != nil {
			return 0, fmt.Errorf("目标表 %s 开启事务失败: %w", tableName, err)
		}

		stmt, err := tx.Prepare(insertSQL)
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("准备插入语句失败: %w", err)
		}
		defer stmt.Close()

		for rows.Next() {
			values, err := scanRowValues(rows, len(columns))
			if err != nil {
				_ = tx.Rollback()
				return insertCount, fmt.Errorf("扫描源数据失败: %w", err)
			}
			if _, err := stmt.Exec(values...); err != nil {
				_ = tx.Rollback()
				return insertCount, fmt.Errorf("写入目标表 %s 失败: %w", tableName, err)
			}
			insertCount++
		}

		if err := rows.Err(); err != nil {
			_ = tx.Rollback()
			return insertCount, fmt.Errorf("遍历源数据失败: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return insertCount, fmt.Errorf("提交目标表 %s 事务失败: %w", tableName, err)
		}
		return insertCount, nil
	}

	stmt, err := m.targetConn.GetDB().Prepare(insertSQL)
	if err != nil {
		return 0, fmt.Errorf("准备插入语句失败: %w", err)
	}
	defer stmt.Close()

	for rows.Next() {
		values, err := scanRowValues(rows, len(columns))
		if err != nil {
			return insertCount, fmt.Errorf("扫描源数据失败: %w", err)
		}
		if _, err := stmt.Exec(values...); err != nil {
			return insertCount, fmt.Errorf("写入目标表 %s 失败: %w", tableName, err)
		}
		insertCount++
	}

	if err := rows.Err(); err != nil {
		return insertCount, fmt.Errorf("遍历源数据失败: %w", err)
	}
	return insertCount, nil
}

// buildInsertSQL 构建目标 MySQL 的参数化插入语句。
func buildInsertSQL(tableName string, columns []string) string {
	quotedCols := make([]string, 0, len(columns))
	holders := make([]string, 0, len(columns))
	for _, col := range columns {
		quotedCols = append(quotedCols, fmt.Sprintf("`%s`", col))
		holders = append(holders, "?")
	}
	return fmt.Sprintf("INSERT INTO `%s` (%s) VALUES (%s)", tableName, strings.Join(quotedCols, ", "), strings.Join(holders, ", "))
}

// scanRowValues 读取当前行并转换为可执行参数列表。
func scanRowValues(rows *sql.Rows, columnCount int) ([]interface{}, error) {
	values := make([]interface{}, columnCount)
	valuePtrs := make([]interface{}, columnCount)
	for i := range values {
		valuePtrs[i] = &values[i]
	}
	if err := rows.Scan(valuePtrs...); err != nil {
		return nil, err
	}

	for i, v := range values {
		if b, ok := v.([]byte); ok {
			cloned := make([]byte, len(b))
			copy(cloned, b)
			values[i] = cloned
		}
	}
	return values, nil
}

// updateProgress 更新任务进度计数。
func (m *Manager) updateProgress() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.completedTasks++
	if m.config.Run.ShowProgress && m.totalTasks > 0 {
		progress := float64(m.completedTasks) / float64(m.totalTasks) * 100
		m.Log("进度: %.2f%% (%d/%d)", progress, m.completedTasks, m.totalTasks)
	}
}

// Log 记录普通日志。
func (m *Manager) Log(format string, args ...interface{}) {
	logMsg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logEntry := fmt.Sprintf("[%s] %s\n", timestamp, logMsg)

	if m.config.Run.EnableFileLogging && m.logFile != nil {
		_, _ = m.logFile.WriteString(logEntry)
	}
	if m.config.Run.ShowLogInConsole {
		fmt.Println(logMsg)
	}
}

// logError 记录错误日志。
func (m *Manager) logError(errMsg string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	logEntry := fmt.Sprintf("[%s] ERROR: %s\n", timestamp, errMsg)
	if m.config.Run.EnableFileLogging && m.errorLogFile != nil {
		_, _ = m.errorLogFile.WriteString(logEntry)
	}
	if m.config.Run.ShowConsoleLogs {
		fmt.Printf("错误: %s\n", errMsg)
	}
}

// displayInconsistentTables 输出数据行数校验不一致的表清单。
func (m *Manager) displayInconsistentTables() {
	if len(m.inconsistentTables) == 0 {
		return
	}
	for _, item := range m.inconsistentTables {
		if item.TargetRowCount == -1 {
			m.logError(fmt.Sprintf("表 %s 数据不一致: source=%d, target=不存在", item.TableName, item.SourceRowCount))
			continue
		}
		m.logError(fmt.Sprintf("表 %s 数据不一致: source=%d, target=%d", item.TableName, item.SourceRowCount, item.TargetRowCount))
	}
}
