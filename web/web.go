// Package web 内嵌前端构建产物（web/dist/app，由 `npm run build` 生成）。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Assets 返回前端静态资源文件系统；ok=false 表示尚未构建前端。
func Assets() (fsys fs.FS, ok bool) {
	sub, err := fs.Sub(distFS, "dist/app")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
