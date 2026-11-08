// Package config 负责加载启动配置（环境变量优先，其次命令行参数）。
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config 是进程级启动配置；运行期可调整的设置存放在数据库 setting 表中。
type Config struct {
	Listen   string // HTTP 监听地址
	DataDir  string // 数据目录：数据库、密钥、日志
	LogLevel string // debug | info | warn | error
	DevMode  bool   // 开发模式：不提供内嵌前端，由 Vite dev server 代理
}

// Load 解析命令行参数与环境变量。环境变量：DKSTORE_LISTEN、DKSTORE_DATA_DIR、DKSTORE_LOG_LEVEL、DKSTORE_DEV。
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("dkstore", flag.ContinueOnError)
	c := &Config{}
	fs.StringVar(&c.Listen, "listen", envOr("DKSTORE_LISTEN", ":8780"), "HTTP 监听地址")
	fs.StringVar(&c.DataDir, "data", envOr("DKSTORE_DATA_DIR", "./data"), "数据目录")
	fs.StringVar(&c.LogLevel, "log-level", envOr("DKSTORE_LOG_LEVEL", "info"), "日志级别")
	fs.BoolVar(&c.DevMode, "dev", envOr("DKSTORE_DEV", "") == "1", "开发模式")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, fmt.Errorf("解析数据目录失败: %w", err)
	}
	c.DataDir = abs
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	c.LogLevel = strings.ToLower(c.LogLevel)
	return c, nil
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
