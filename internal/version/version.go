package version

import (
	"fmt"
	"runtime"
)

var (
	// Version 是程序的版本号
	Version = "1.0.0"
	// BuildTime 是程序构建的时间
	BuildTime = "unknown"
	// GitCommit 是程序构建时的 Git Commit ID
	GitCommit = "unknown"
	// GoVersion 是构建该程序时使用的 Go 版本
	GoVersion = runtime.Version()
)

// PrintVersion 打印程序的版本信息
func PrintVersion() {
	fmt.Printf("MySQL2PG 版本: %s\n", Version)
	fmt.Printf("Git Commit:   %s\n", GitCommit)
	fmt.Printf("构建时间:     %s\n", BuildTime)
	fmt.Printf("Go 版本:       %s\n", GoVersion)
}
