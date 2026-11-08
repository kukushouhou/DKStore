// Package version 保存构建版本信息，通过 -ldflags -X 注入。
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
