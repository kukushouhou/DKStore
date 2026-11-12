//go:build ignore

// build_fpk.go 打包生成飞牛 fnOS 可直接手动安装的 .fpk 应用包（兼容 fnpack 1.2.3 规范，无需依赖 Linux 环境）。
// 用法：go run ./packaging/fnos/build_fpk.go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	rootDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	distDir := filepath.Join(rootDir, "dist")
	_ = os.MkdirAll(distDir, 0o755)

	targets := []struct {
		goarch   string
		platform string
		fpkName  string
	}{
		{
			goarch:   "amd64",
			platform: "x86",
			fpkName:  "dkstore-fnos-x86-0.2.0.fpk",
		},
		{
			goarch:   "arm64",
			platform: "arm",
			fpkName:  "dkstore-fnos-arm-0.2.0.fpk",
		},
	}

	for _, t := range targets {
		fpkPath := filepath.Join(distDir, t.fpkName)
		if err := buildFPK(rootDir, fpkPath, t.goarch, t.platform); err != nil {
			panic(err)
		}
		fmt.Printf("Built FPK: %s\n", fpkPath)
	}
}

func buildFPK(rootDir, outPath, goarch, fnPlatform string) error {
	tmpBin := filepath.Join(os.TempDir(), "dkstore-fnos-"+goarch)
	defer os.Remove(tmpBin)

	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X github.com/kukushouhou/DKStore/internal/version.Version=0.2.0", "-o", tmpBin, "./cmd/dkstore")
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build (%s) failed: %v\n%s", goarch, err, string(out))
	}
	binBytes, err := os.ReadFile(tmpBin)
	if err != nil {
		return err
	}

	uiConfig, err := os.ReadFile(filepath.Join(rootDir, "packaging", "fnos", "ui", "config"))
	if err != nil {
		return err
	}
	uiConfig = stripBOM(lf(uiConfig))

	privBytes, err := os.ReadFile(filepath.Join(rootDir, "packaging", "fnos", "config", "privilege"))
	if err != nil {
		return err
	}
	privBytes = stripBOM(lf(privBytes))

	resBytes, err := os.ReadFile(filepath.Join(rootDir, "packaging", "fnos", "config", "resource"))
	if err != nil {
		return err
	}
	resBytes = stripBOM(lf(resBytes))

	mainCmdBytes, err := os.ReadFile(filepath.Join(rootDir, "packaging", "fnos", "cmd", "main"))
	if err != nil {
		return err
	}
	mainCmdBytes = stripBOM(lf(mainCmdBytes))

	icon64 := renderIconPNG(64)
	icon256 := renderIconPNG(256)

	// 1. 生成内层 app.tgz（包含 server/dkstore、ui/config、ui/images/icon_{64,256}.png、config/{privilege,resource}）
	var appTgzBuf bytes.Buffer
	appGz := gzip.NewWriter(&appTgzBuf)
	appTw := tar.NewWriter(appGz)

	addTarDir(appTw, "server")
	addTarFile(appTw, "server/dkstore", binBytes, 0o755)
	addTarDir(appTw, "ui")
	addTarFile(appTw, "ui/config", uiConfig, 0o644)
	addTarDir(appTw, "ui/images")
	addTarFile(appTw, "ui/images/icon_64.png", icon64, 0o644)
	addTarFile(appTw, "ui/images/icon_256.png", icon256, 0o644)
	addTarDir(appTw, "config")
	addTarFile(appTw, "config/privilege", privBytes, 0o644)
	addTarFile(appTw, "config/resource", resBytes, 0o644)

	if err := appTw.Close(); err != nil {
		return err
	}
	if err := appGz.Close(); err != nil {
		return err
	}
	appTgzBytes := appTgzBuf.Bytes()
	sum := md5.Sum(appTgzBytes)
	checksum := hex.EncodeToString(sum[:])

	// 2. 生成 manifest（UTF-8 无 BOM，LF 换行）
	manifestLines := []string{
		"appname               = dkstore",
		"version               = 0.2.0",
		"display_name          = DKStore 容器更新中心",
		"desc                  = 跨平台 Docker 应用更新中心：支持本机与远程 Docker/Compose 发现、单服务安全更新、版本切换与历史回退。",
		fmt.Sprintf("platform              = %s", fnPlatform),
		"source                = thirdparty",
		"maintainer            = kukushouhou",
		"maintainer_url        = https://github.com/kukushouhou",
		"distributor           = kukushouhou",
		"distributor_url       = https://github.com/kukushouhou/DKStore",
		"desktop_uidir         = ui",
		"desktop_applaunchname = dkstore.Application",
		"service_port          = 8090",
		"os_min_version        = 1.1.3100",
		"ctl_stop              = true",
		fmt.Sprintf("checksum              = %s", checksum),
		"",
	}
	manifestBytes := []byte(strings.Join(manifestLines, "\n"))
	noopHook := []byte("#!/bin/bash\nexit 0\n")

	// 3. 打包外层 .fpk（gzip 压缩 tar 归档，与 fnpack 1.2.3 完全一致）
	var fpkBuf bytes.Buffer
	fpkGz := gzip.NewWriter(&fpkBuf)
	fpkTw := tar.NewWriter(fpkGz)

	addTarFile(fpkTw, "app.tgz", appTgzBytes, 0o644)
	addTarDir(fpkTw, "cmd")
	for _, hook := range []string{
		"config_callback", "config_init",
		"install_callback", "install_init",
		"uninstall_callback", "uninstall_init",
		"upgrade_callback", "upgrade_init",
	} {
		addTarFile(fpkTw, "cmd/"+hook, noopHook, 0o755)
	}
	addTarFile(fpkTw, "cmd/main", mainCmdBytes, 0o755)
	addTarDir(fpkTw, "config")
	addTarFile(fpkTw, "config/privilege", privBytes, 0o644)
	addTarFile(fpkTw, "config/resource", resBytes, 0o644)
	addTarFile(fpkTw, "ICON.PNG", icon64, 0o644)
	addTarFile(fpkTw, "ICON_256.PNG", icon256, 0o644)
	addTarFile(fpkTw, "manifest", manifestBytes, 0o644)
	addTarDir(fpkTw, "wizard")

	if err := fpkTw.Close(); err != nil {
		return err
	}
	if err := fpkGz.Close(); err != nil {
		return err
	}
	return os.WriteFile(outPath, fpkBuf.Bytes(), 0o644)
}

func lf(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}

func addTarDir(tw *tar.Writer, name string) {
	_ = tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Now(),
	})
}

func addTarFile(tw *tar.Writer, name string, data []byte, mode int64) {
	_ = tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	})
	_, _ = tw.Write(data)
}

func renderIconPNG(size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{R: 22, G: 119, B: 255, A: 255}
	fg := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	radius := size / 5
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if inRoundedRect(x, y, size, size, radius) {
				img.SetRGBA(x, y, bg)
			}
		}
	}
	pad := size / 5
	cellW := (size - 2*pad) / 3
	cellH := (size - 2*pad) / 3
	gap := max(1, size/24)
	for row := 0; row < 2; row++ {
		for col := 0; col < 3; col++ {
			x0 := pad + col*cellW + gap
			y0 := pad + (row+1)*cellH + gap - cellH/2
			x1 := pad + (col+1)*cellW - gap
			y1 := y0 + cellH - 2*gap
			for y := y0; y < y1 && y < size; y++ {
				for x := x0; x < x1 && x < size; x++ {
					if x >= 0 && y >= 0 {
						img.SetRGBA(x, y, fg)
					}
				}
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func inRoundedRect(x, y, w, h, r int) bool {
	cx, cy := x, y
	if x < r {
		cx = r
	} else if x >= w-r {
		cx = w - r - 1
	}
	if y < r {
		cy = r
	} else if y >= h-r {
		cy = h - r - 1
	}
	dx := x - cx
	dy := y - cy
	return dx*dx+dy*dy <= r*r
}
