package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TableInfo 表信息
type TableInfo struct {
	Name    string
	DDL     string
	Columns []ColumnInfo
	Indexes []IndexInfo
}

// ColumnInfo 列信息
type ColumnInfo struct {
	Name     string
	Type     string
	Nullable string
	Default  *string
	Comment  string
}

// IndexInfo 索引信息
type IndexInfo struct {
	Name     string
	Table    string
	Columns  []string
	IsUnique bool
}

// FunctionInfo 函数信息
type FunctionInfo struct {
	Name       string
	DDL        string
	Parameters string
	ReturnType string
}

// UserInfo 用户信息
type UserInfo struct {
	Name   string
	Grants []string
}

// ViewInfo 视图信息
type ViewInfo struct {
	ViewName       string
	ViewDefinition string
}

// GetTables 获取所有表信息（浅层信息，仅获取表名）
func (c *Connection) GetTables(skipUseTableList bool, skipTableList []string, useTableList bool, tableList []string) ([]TableInfo, error) {
	// 创建一个带超时时间（1分钟）的上下文用于获取表列表
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 获取当前连接的用户名，以便更好地诊断权限问题
	var currentUser string
	if err := c.db.QueryRowContext(ctx, "SELECT USER()").Scan(&currentUser); err != nil {
		return nil, fmt.Errorf("获取当前用户名失败: %w", err)
	}

	// 使用多种方法尝试获取表列表，以兼容不同的MySQL版本和权限配置
	var rows *sql.Rows
	var err error

	// 使用INFORMATION_SCHEMA.TABLES查询，只获取TABLE类型的对象，过滤掉视图
	query := "SELECT table_name FROM INFORMATION_SCHEMA.TABLES WHERE table_schema = ? AND table_type = 'BASE TABLE'"
	args := []interface{}{c.config.Database}

	// 在SQL层面过滤只同步的表
	if useTableList && len(tableList) > 0 {
		var conditions []string
		for _, t := range tableList {
			if strings.Contains(t, "%") || strings.Contains(t, "_") {
				conditions = append(conditions, "table_name LIKE ?")
			} else {
				conditions = append(conditions, "table_name = ?")
			}
			args = append(args, t)
		}
		query += " AND (" + strings.Join(conditions, " OR ") + ")"
	}

	// 在SQL层面过滤掉需要跳过的表
	if skipUseTableList && len(skipTableList) > 0 {
		var conditions []string
		for _, t := range skipTableList {
			if strings.Contains(t, "%") || strings.Contains(t, "_") {
				conditions = append(conditions, "table_name NOT LIKE ?")
			} else {
				conditions = append(conditions, "table_name <> ?")
			}
			args = append(args, t)
		}
		query += " AND (" + strings.Join(conditions, " AND ") + ")"
	}

	// 增加重试逻辑以处理网络抖动或大数据库下的慢查询
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		rows, err = c.db.QueryContext(ctx, query, args...)
		if err == nil {
			break
		}
		if i == maxRetries-1 {
			return nil, fmt.Errorf("获取表列表失败 (已重试 %d 次): %w。当前用户: %s，数据库: %s", maxRetries, err, currentUser, c.config.Database)
		}
		time.Sleep(2 * time.Second) // 等待一下再重试
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, fmt.Errorf("扫描表名失败: %w", err)
		}
		tables = append(tables, TableInfo{Name: tableName})
	}

	return tables, nil
}

// GetTableDDL 获取单个表的 DDL
func (c *Connection) GetTableDDL(tableName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var ddl string
	query := fmt.Sprintf("SHOW CREATE TABLE `%s`", tableName)

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("获取表 %s DDL 失败: %w", tableName, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}

	if !rows.Next() {
		return "", fmt.Errorf("SHOW CREATE TABLE %s 没有返回结果", tableName)
	}

	vals := make([]interface{}, len(columns))
	valPtrs := make([]*string, len(columns))
	for i := range vals {
		valPtrs[i] = new(string)
		vals[i] = valPtrs[i]
	}

	if err := rows.Scan(vals...); err != nil {
		return "", err
	}

	if len(valPtrs) > 1 && *valPtrs[1] != "" {
		ddl = *valPtrs[1]
	} else if len(valPtrs) > 3 && *valPtrs[3] != "" {
		ddl = *valPtrs[3]
	}

	return ddl, nil
}

// GetAllIndexes 获取整个数据库的所有索引（排除主键）
func (c *Connection) GetAllIndexes(database string, tableNames []string) ([]IndexInfo, error) {
	// 如果表名过多，分批查询以防 SQL 语句过长
	const batchSize = 1000
	var allIndexes []IndexInfo

	for i := 0; i < len(tableNames); i += batchSize {
		end := i + batchSize
		if end > len(tableNames) {
			end = len(tableNames)
		}
		batch := tableNames[i:end]

		placeholders := make([]string, len(batch))
		args := []interface{}{database}
		for j := range batch {
			placeholders[j] = "?"
			args = append(args, batch[j])
		}

		query := fmt.Sprintf(`
			SELECT table_name, index_name, non_unique, column_name, seq_in_index 
			FROM information_schema.statistics 
			WHERE table_schema = ? AND table_name IN (%s)
			ORDER BY table_name, index_name, seq_in_index
		`, strings.Join(placeholders, ","))

		rows, err := c.db.Query(query, args...)
		if err != nil {
			return nil, fmt.Errorf("批量获取索引失败: %w", err)
		}

		indexMap := make(map[string]*IndexInfo)
		for rows.Next() {
			var tableName, indexName, columnName string
			var nonUnique int
			var seqInIndex sql.NullString

			if err := rows.Scan(&tableName, &indexName, &nonUnique, &columnName, &seqInIndex); err != nil {
				rows.Close()
				return nil, err
			}

			// 排除主键
			if indexName == "PRIMARY" {
				continue
			}

			key := tableName + "." + indexName
			if _, exists := indexMap[key]; !exists {
				indexMap[key] = &IndexInfo{
					Name:     indexName,
					Table:    tableName,
					IsUnique: nonUnique == 0,
				}
			}
			indexMap[key].Columns = append(indexMap[key].Columns, columnName)
		}
		rows.Close()

		for _, idx := range indexMap {
			allIndexes = append(allIndexes, *idx)
		}
	}

	return allIndexes, nil
}

// getTableColumns 获取表的列信息
func (c *Connection) getTableColumns(tableName string) ([]ColumnInfo, error) {
	// 使用反引号包围表名，以处理包含特殊字符的表名
	query := fmt.Sprintf("SHOW FULL COLUMNS FROM `%s`", tableName)
	rows, err := c.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []ColumnInfo
	for rows.Next() {
		var col ColumnInfo
		var field, colType, null, key, extra, comment string
		var defaultValue sql.NullString
		var collation, privileges sql.NullString

		if err := rows.Scan(&field, &colType, &collation, &null, &key, &defaultValue, &extra, &privileges, &comment); err != nil {
			return nil, err
		}

		col.Name = field
		col.Type = colType
		col.Nullable = null
		col.Comment = comment

		if defaultValue.Valid {
			col.Default = &defaultValue.String
		}

		columns = append(columns, col)
	}

	return columns, nil
}

// getTableIndexes 获取表的索引信息
func (c *Connection) getTableIndexes(tableName string) ([]IndexInfo, error) {
	// 使用information_schema.statistics查询索引信息，兼容MySQL 5.7和MySQL 8.0
	// 只查询需要的字段：table_name, index_name, non_unique, column_name, seq_in_index
	query := `
		SELECT table_name, index_name, non_unique, column_name, seq_in_index 
		FROM information_schema.statistics 
		WHERE table_schema = ? AND table_name = ? 
		ORDER BY index_name, seq_in_index
	`
	rows, err := c.db.Query(query, c.config.Database, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 使用map来按索引名分组
	indexMap := make(map[string]*IndexInfo)

	for rows.Next() {
		var tableName, indexName, columnName string
		var nonUnique int
		var seqInIndex sql.NullString

		if err := rows.Scan(&tableName, &indexName, &nonUnique, &columnName, &seqInIndex); err != nil {
			return nil, err
		}

		if _, exists := indexMap[indexName]; !exists {
			indexMap[indexName] = &IndexInfo{
				Name:     indexName,
				Table:    tableName,
				IsUnique: nonUnique == 0,
			}
		}

		indexMap[indexName].Columns = append(indexMap[indexName].Columns, columnName)
	}

	// 将map转换为slice
	var indexes []IndexInfo
	for _, idx := range indexMap {
		indexes = append(indexes, *idx)
	}

	return indexes, nil
}

// GetViews 获取所有视图信息
func (c *Connection) GetViews(database string) ([]ViewInfo, error) {
	// 查询视图定义
	query := `
		SELECT table_name, view_definition 
		FROM INFORMATION_SCHEMA.VIEWS 
		WHERE table_schema = ?
	`
	rows, err := c.db.Query(query, database)
	if err != nil {
		return nil, fmt.Errorf("查询视图定义失败: %w", err)
	}
	defer rows.Close()

	var views []ViewInfo
	for rows.Next() {
		var view ViewInfo
		if err := rows.Scan(&view.ViewName, &view.ViewDefinition); err != nil {
			return nil, fmt.Errorf("扫描视图信息失败: %w", err)
		}
		views = append(views, view)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历视图结果失败: %w", err)
	}

	return views, nil
}

// GetFunctions 获取所有函数信息
func (c *Connection) GetFunctions() ([]FunctionInfo, error) {
	// 使用SHOW FUNCTION STATUS获取函数列表，避免查询information_schema导致的权限问题
	// 这样可以同时兼容MySQL 5.7和MySQL 8.0
	query := fmt.Sprintf("SHOW FUNCTION STATUS WHERE Db = '%s'", c.config.Database)

	rows, err := c.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("获取函数列表失败: %w", err)
	}
	defer rows.Close()

	// 获取列信息，只需要调用一次
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("获取函数状态结果列信息失败: %w", err)
	}

	var functionNames []string
	for rows.Next() {
		// 创建足够的空指针来存储结果
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		// 扫描结果
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("扫描函数状态信息失败: %w", err)
		}

		// 提取函数名（Name字段在第2个位置）
		var name string
		if len(columns) > 1 {
			// 使用通用的类型转换方法
			switch v := values[1].(type) {
			case []byte:
				name = string(v)
			case string:
				name = v
			default:
				name = fmt.Sprintf("%v", v)
			}
		}

		if name != "" {
			functionNames = append(functionNames, name)
		}
	}

	// 检查是否有错误发生
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("迭代函数列表时发生错误: %w", err)
	}

	var functions []FunctionInfo
	for _, funcName := range functionNames {
		// 使用SHOW CREATE FUNCTION获取函数定义
		funcQuery := fmt.Sprintf("SHOW CREATE FUNCTION `%s`", funcName)
		funcRows, err := c.db.Query(funcQuery)
		if err != nil {
			// 如果获取某个函数的定义失败，跳过该函数，继续处理其他函数
			continue
		}

		// 先检查是否有结果行
		if !funcRows.Next() {
			funcRows.Close()
			continue
		}

		// 使用动态方式处理SHOW CREATE FUNCTION的结果，避免不同MySQL版本返回不同字段数的问题
		columns, err := funcRows.Columns()
		if err != nil {
			funcRows.Close()
			continue
		}

		// 创建足够的空指针来存储结果
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range columns {
			valuePtrs[i] = &values[i]
		}

		// 扫描结果
		if err := funcRows.Scan(valuePtrs...); err != nil {
			funcRows.Close()
			continue
		}

		funcRows.Close()

		// 解析结果，寻找Function和Create Function字段
		var name, definition string
		for i, col := range columns {
			var value string
			if values[i] == nil {
				value = ""
			} else if b, ok := values[i].([]byte); ok {
				value = string(b)
			} else if v, ok := values[i].(string); ok {
				value = v
			} else {
				value = fmt.Sprintf("%v", values[i])
			}

			// 根据列名确定字段值
			switch strings.ToLower(col) {
			case "function":
				name = value
			case "create function":
				definition = value
			default:
				// 忽略其他字段
			}
		}

		if name != "" && definition != "" {
			// 简单处理返回类型
			returnType := ""

			// 从函数体中解析参数
			parameters := ""
			if idx := strings.Index(definition, "("); idx != -1 {
				// 寻找匹配的右括号
				count := 1
				endIdx := idx + 1
				for endIdx < len(definition) {
					if definition[endIdx] == '(' {
						count++
					} else if definition[endIdx] == ')' {
						count--
						if count == 0 {
							break
						}
					}
					endIdx++
				}
				if endIdx < len(definition) {
					parameters = definition[idx+1 : endIdx]
				}
			}

			functions = append(functions, FunctionInfo{
				Name:       name,
				DDL:        definition,
				Parameters: parameters,
				ReturnType: returnType,
			})
		}
	}

	return functions, nil
}

// GetUsers 获取所有用户信息
func (c *Connection) GetUsers() ([]UserInfo, error) {
	// MySQL中获取用户权限
	rows, err := c.db.Query(`
		SELECT user, host 
		FROM mysql.user 
		WHERE user != 'root' AND user != 'mysql.sys' AND user != 'mysql.session'
	`)
	if err != nil {
		return nil, fmt.Errorf("获取用户列表失败: %w", err)
	}
	defer rows.Close()

	var users []UserInfo
	for rows.Next() {
		var userName, host string
		if err := rows.Scan(&userName, &host); err != nil {
			return nil, fmt.Errorf("扫描用户信息失败: %w", err)
		}

		// 获取用户权限
		grants, err := c.getUserGrants(userName, host)
		if err != nil {
			return nil, fmt.Errorf("获取用户权限失败: %w", err)
		}

		users = append(users, UserInfo{
			Name:   fmt.Sprintf("%s@%s", userName, host),
			Grants: grants,
		})
	}

	return users, nil
}

// getUserGrants 获取用户的权限信息
func (c *Connection) getUserGrants(userName, host string) ([]string, error) {
	var grantsStr string
	// 直接使用字符串拼接构建查询语句
	grantQuery := fmt.Sprintf("SHOW GRANTS FOR '%s'@'%s'", userName, host)
	err := c.db.QueryRow(grantQuery).Scan(&grantsStr)
	if err != nil {
		return nil, err
	}

	// 解析权限字符串
	grants := strings.Split(grantsStr, ";")
	var cleanGrants []string
	for _, grant := range grants {
		grant = strings.TrimSpace(grant)
		if grant != "" {
			cleanGrants = append(cleanGrants, grant)
		}
	}

	return cleanGrants, nil
}

// TablePrivInfo 表权限信息
type TablePrivInfo struct {
	Host      string
	Db        string
	User      string
	TableName string
	TablePriv string
}

// GetTablePrivileges 获取表权限信息
func (c *Connection) GetTablePrivileges() ([]TablePrivInfo, error) {
	query := `
		SELECT Host, Db, User, Table_name, Table_priv 
		FROM mysql.tables_priv 
		WHERE Table_priv != ''
	`

	rows, err := c.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("获取表权限失败: %w", err)
	}
	defer rows.Close()

	var privileges []TablePrivInfo
	for rows.Next() {
		var priv TablePrivInfo
		if err := rows.Scan(&priv.Host, &priv.Db, &priv.User, &priv.TableName, &priv.TablePriv); err != nil {
			return nil, fmt.Errorf("扫描表权限信息失败: %w", err)
		}

		privileges = append(privileges, priv)
	}

	return privileges, nil
}
