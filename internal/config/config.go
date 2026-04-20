package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config 存储所有配置
type Config struct {
	MySQL            MySQLConfig      `mapstructure:"mysql"`
	TargetMySQL      MySQLConfig      `mapstructure:"target_mysql"`
	TargetPostgreSQL PostgreSQLConfig `mapstructure:"target_postgresql"`
	Conversion       ConversionConfig `mapstructure:"conversion"`
	Run              RunConfig        `mapstructure:"run"`
}

// MySQLConfig MySQL连接配置
type MySQLConfig struct {
	Host             string `mapstructure:"host"`
	Port             int    `mapstructure:"port"`
	Username         string `mapstructure:"username"`
	Password         string `mapstructure:"password"`
	Database         string `mapstructure:"database"`
	TestOnly         bool   `mapstructure:"test_only"`
	MaxOpenConns     int    `mapstructure:"max_open_conns"`    // 最大打开连接数
	MaxIdleConns     int    `mapstructure:"max_idle_conns"`    // 最大空闲连接数
	ConnMaxLifetime  int    `mapstructure:"conn_max_lifetime"` // 连接最大生命周期（秒）
	ConnectionParams string `mapstructure:"connection_params"` // MySQL连接参数
}

type PostgreSQLConfig struct {
	Host               string `mapstructure:"host"`
	Port               int    `mapstructure:"port"`
	Username           string `mapstructure:"username"`
	Password           string `mapstructure:"password"`
	Database           string `mapstructure:"database"`
	TestOnly           bool   `mapstructure:"test_only"`
	MaxConns           int    `mapstructure:"max_conns"`            // 最大连接数
	PgConnectionParams string `mapstructure:"pg_connection_params"` // PostgreSQL连接参数
}

// ConversionConfig 转换配置
type ConversionConfig struct {
	Options OptionsConfig `mapstructure:"options"`
	Limits  LimitsConfig  `mapstructure:"limits"`
}

// OptionsConfig 转换选项配置
type OptionsConfig struct {
	Functions          bool     `mapstructure:"functions"`
	Indexes            bool     `mapstructure:"indexes"`
	Users              bool     `mapstructure:"users"`
	TableDDL           bool     `mapstructure:"tableddl"`               // 转换表DDL
	Data               bool     `mapstructure:"data"`                   // 转换数据
	View               bool     `mapstructure:"view"`                   // 转换视图
	Grant              bool     `mapstructure:"grant"`                  // 转换权限
	TablePrivileges    bool     `mapstructure:"table_privileges"`       // 转换表权限
	Compare            bool     `mapstructure:"compare"`                // 仅对比数据差异
	SkipExistingTables bool     `mapstructure:"skip_existing_tables"`   // 跳过已存在的表
	UseTableList       bool     `mapstructure:"use_table_list"`         // 是否使用指定的表列表进行数据同步
	TableList          []string `mapstructure:"table_list"`             // 指定要同步的表列表
	SkipUseTableList   bool     `mapstructure:"exclude_use_table_list"` // 是否使用跳过表列表
	SkipTableList      []string `mapstructure:"exclude_table_list"`     // 要跳过的表列表
	ValidateData       bool     `mapstructure:"validate_data"`          // 同步后验证数据一致性
	LowercaseColumns   bool     `mapstructure:"lowercase_columns"`      // 表字段是否转小写，true代表转小写，默认，false代表与mysql一致
	TruncateBeforeSync bool     `mapstructure:"truncate_before_sync"`   // 同步前是否清空表数据
	UseTransaction     bool     `mapstructure:"use_transaction"`        // 同步数据时是否开启事务
}

// LimitsConfig 限制配置
type LimitsConfig struct {
	Concurrency          int `mapstructure:"concurrency"`
	BandwidthMbps        int `mapstructure:"bandwidth_mbps"`
	MaxDDLPerBatch       int `mapstructure:"max_ddl_per_batch"`
	MaxFunctionsPerBatch int `mapstructure:"max_functions_per_batch"`
	MaxIndexesPerBatch   int `mapstructure:"max_indexes_per_batch"`
	MaxUsersPerBatch     int `mapstructure:"max_users_per_batch"`
	MaxRowsPerBatch      int `mapstructure:"max_rows_per_batch"` // 一次性同步数据的行数限制
	BatchInsertSize      int `mapstructure:"batch_insert_size"`  // 批量插入的大小
}

// RunConfig 运行配置
type RunConfig struct {
	ShowProgress      bool   `mapstructure:"show_progress"`
	ErrorLogPath      string `mapstructure:"error_log_path"`
	EnableFileLogging bool   `mapstructure:"enable_file_logging"`
	LogFilePath       string `mapstructure:"log_file_path"`
	ShowConsoleLogs   bool   `mapstructure:"show_console_logs"`
	ShowLogInConsole  bool   `mapstructure:"show_log_in_console"`
	CompareResultPath string `mapstructure:"compare_result_path"` // 对比结果CSV导出路径
}

// LoadConfig 加载配置文件
func LoadConfig(configPath string) (*Config, error) {
	// 如果没有指定配置文件路径，尝试在当前目录查找
	if configPath == "" {
		currentDir, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("无法获取当前目录: %w", err)
		}

		// 尝试查找config.yml或config.yaml
		configPaths := []string{
			filepath.Join(currentDir, "config.yml"),
			filepath.Join(currentDir, "config.yaml"),
		}

		found := false
		for _, path := range configPaths {
			if _, err := os.Stat(path); err == nil {
				configPath = path
				found = true
				break
			}
		}

		if !found {
			return nil, fmt.Errorf("未找到配置文件，请指定配置文件路径或在当前目录创建config.yml")
		}
	}

	viper.SetConfigFile(configPath)
	viper.SetDefault("conversion.options.use_transaction", true)

	// 读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return &config, nil
}

// ValidateConfig 验证配置是否有效
func (c *Config) ValidateConfig() error {
	// 验证MySQL配置
	if c.MySQL.Host == "" {
		return fmt.Errorf("MySQL主机地址不能为空")
	}
	if c.MySQL.Username == "" {
		return fmt.Errorf("MySQL用户名不能为空")
	}
	if c.MySQL.Database == "" {
		return fmt.Errorf("MySQL数据库名不能为空")
	}
	// MySQL连接池默认值
	if c.MySQL.MaxOpenConns <= 0 {
		c.MySQL.MaxOpenConns = 100 // 默认值从50提高到100
	}
	if c.MySQL.MaxIdleConns <= 0 {
		c.MySQL.MaxIdleConns = 50 // 默认值从20提高到50
	}
	if c.MySQL.ConnMaxLifetime <= 0 {
		c.MySQL.ConnMaxLifetime = 1800 // 默认值从3600降低到1800，以防MySQL server端的wait_timeout更短
	}

	// 根据配置决定目标库类型：
	// 1) 如果显式配置了target_mysql，则走MySQL->MySQL
	// 2) 否则走MySQL->PostgreSQL
	if c.UseTargetMySQL() && c.UseTargetPostgreSQL() {
		return fmt.Errorf("target_mysql 与 target_postgresql 不能同时配置")
	}

	if c.UseTargetMySQL() {
		if c.TargetMySQL.Host == "" {
			return fmt.Errorf("target_mysql主机地址不能为空")
		}
		if c.TargetMySQL.Username == "" {
			return fmt.Errorf("target_mysql用户名不能为空")
		}
		if c.TargetMySQL.Database == "" {
			return fmt.Errorf("target_mysql数据库名不能为空")
		}
		if c.TargetMySQL.MaxOpenConns <= 0 {
			c.TargetMySQL.MaxOpenConns = 100
		}
		if c.TargetMySQL.MaxIdleConns <= 0 {
			c.TargetMySQL.MaxIdleConns = 50
		}
		if c.TargetMySQL.ConnMaxLifetime <= 0 {
			c.TargetMySQL.ConnMaxLifetime = 1800
		}
	} else {
		// 验证PostgreSQL配置
		if c.TargetPostgreSQL.Host == "" {
			return fmt.Errorf("PostgreSQL主机地址不能为空")
		}
		if c.TargetPostgreSQL.Username == "" {
			return fmt.Errorf("PostgreSQL用户名不能为空")
		}
		if c.TargetPostgreSQL.Database == "" {
			return fmt.Errorf("PostgreSQL数据库名不能为空")
		}
		// PostgreSQL连接池默认值
		if c.TargetPostgreSQL.MaxConns <= 0 {
			c.TargetPostgreSQL.MaxConns = 20 // 默认值
		}
	}

	// 验证转换限制
	if c.Conversion.Limits.Concurrency <= 0 {
		c.Conversion.Limits.Concurrency = 1 // 默认值
	}
	if c.Conversion.Limits.MaxDDLPerBatch <= 0 {
		c.Conversion.Limits.MaxDDLPerBatch = 10 // 默认值
	}
	if c.Conversion.Limits.MaxFunctionsPerBatch <= 0 {
		c.Conversion.Limits.MaxFunctionsPerBatch = 5 // 默认值
	}
	if c.Conversion.Limits.MaxIndexesPerBatch <= 0 {
		c.Conversion.Limits.MaxIndexesPerBatch = 20 // 默认值
	}
	if c.Conversion.Limits.MaxUsersPerBatch <= 0 {
		c.Conversion.Limits.MaxUsersPerBatch = 10 // 默认值
	}
	if c.Conversion.Limits.MaxRowsPerBatch <= 0 {
		c.Conversion.Limits.MaxRowsPerBatch = 1000 // 默认值
	}

	return nil
}

// UseTargetMySQL 判断是否启用MySQL作为目标库。
func (c *Config) UseTargetMySQL() bool {
	return c.TargetMySQL.Host != "" || c.TargetMySQL.Username != "" || c.TargetMySQL.Database != ""
}

// UseTargetPostgreSQL 判断是否启用PostgreSQL作为目标库。
func (c *Config) UseTargetPostgreSQL() bool {
	return c.TargetPostgreSQL.Host != "" || c.TargetPostgreSQL.Username != "" || c.TargetPostgreSQL.Database != ""
}
