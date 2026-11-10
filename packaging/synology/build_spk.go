//go:build ignore

// build_spk.go 打包生成群晖 DSM 7 可直接手动安装的 .spk 套件包（无需依赖 Linux fakeroot/tar）。
// 用法：go run ./packaging/synology/build_spk.go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/base64"
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
		goarch  string
		spkName string
		synoArch string
	}{
		{
			goarch:   "amd64",
			spkName:  "dkstore-x86_64-0.2.0-0001.spk",
			synoArch: "x86_64 apollolake avoton braswell broadwell broadwellnk broadwellnkv2 broadwellntbap bromolow cedarview denverton epyc7002 geminilake geminilakenk grantley icelaked kvmx64 purley r1000 r1000nk v1000 v1000nk",
		},
		{
			goarch:   "arm64",
			spkName:  "dkstore-aarch64-0.2.0-0001.spk",
			synoArch: "aarch64 armv8 rtd1296 rtd1619b armada37xx",
		},
	}

	for _, t := range targets {
		spkPath := filepath.Join(distDir, t.spkName)
		if err := buildSPK(rootDir, spkPath, t.goarch, t.synoArch); err != nil {
			panic(err)
		}
		fmt.Printf("Built SPK: %s\n", spkPath)
	}
}

func buildSPK(rootDir, outPath, goarch, synoArch string) error {
	tmpBin := filepath.Join(os.TempDir(), "dkstore-linux-"+goarch)
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

	uiConfig, err := os.ReadFile(filepath.Join(rootDir, "packaging", "synology", "ui", "config"))
	if err != nil {
		return err
	}
	uiConfig = lf(uiConfig)
	nginxConf, err := os.ReadFile(filepath.Join(rootDir, "packaging", "synology", "etc", "www", "dsm.dkstore.conf"))
	if err != nil {
		return err
	}
	nginxConf = lf(nginxConf)

	// 1. 生成 package.tgz（包含 bin/dkstore、etc/www/dsm.dkstore.conf、ui/config、ui/images/icon_*.png）
	var pkgTgzBuf bytes.Buffer
	gw := gzip.NewWriter(&pkgTgzBuf)
	ptw := tar.NewWriter(gw)

	addTarDir(ptw, "bin/")
	addTarFile(ptw, "bin/dkstore", binBytes, 0o755)
	addTarDir(ptw, "etc/")
	addTarDir(ptw, "etc/www/")
	addTarFile(ptw, "etc/www/dsm.dkstore.conf", nginxConf, 0o644)
	addTarDir(ptw, "ui/")
	addTarFile(ptw, "ui/config", uiConfig, 0o644)
	addTarDir(ptw, "ui/images/")
	for _, sz := range []int{16, 24, 32, 48, 64, 72, 256} {
		addTarFile(ptw, fmt.Sprintf("ui/images/icon_%d.png", sz), renderIconPNG(sz), 0o644)
	}
	if err := ptw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	pkgTgzBytes := pkgTgzBuf.Bytes()
	sum := md5.Sum(pkgTgzBytes)
	checksum := hex.EncodeToString(sum[:])

	icon72 := renderIconPNG(72)
	icon256 := renderIconPNG(256)

	// 2. 生成 INFO
	infoLines := []string{
		`package="dkstore"`,
		`version="0.2.0-0003"`,
		`os_min_ver="7.0-40000"`,
		`description="DKStore: Cross-Platform Docker App Update Center with One-Click Upgrade & Rollback."`,
		`description_chs="DKStore：跨平台 Docker 应用更新中心（支持群晖 Container Manager 项目识别、单服务安全升级与历史回退）。"`,
		fmt.Sprintf(`arch="%s"`, synoArch),
		`maintainer="kukushouhou"`,
		`maintainer_url="https://github.com/kukushouhou"`,
		`distributor="kukushouhou"`,
		`distributor_url="https://github.com/kukushouhou/DKStore"`,
		`displayname="DKStore"`,
		`displayname_chs="DKStore 容器更新中心"`,
		`dsmuidir="ui"`,
		`dsmappname="SYNO.SDS.DKStore.Application"`,
		`adminurl="/dkstore/"`,
		`startstop_restart_services="nginx.service"`,
		`checkport="no"`,
		`thirdparty="yes"`,
		`reloadui="yes"`,
		`silent_install="yes"`,
		`silent_upgrade="yes"`,
		`silent_uninstall="yes"`,
		fmt.Sprintf(`extractsize="%d"`, (len(binBytes)/1024)+1024),
		fmt.Sprintf(`checksum="%s"`, checksum),
		fmt.Sprintf(`package_icon="%s"`, base64.StdEncoding.EncodeToString(icon72)),
		fmt.Sprintf(`package_icon_256="%s"`, base64.StdEncoding.EncodeToString(icon256)),
		"",
	}
	infoBytes := []byte(strings.Join(infoLines, "\n"))

	privBytes, err := os.ReadFile(filepath.Join(rootDir, "packaging", "synology", "conf", "privilege"))
	if err != nil {
		return err
	}
	resBytes, err := os.ReadFile(filepath.Join(rootDir, "packaging", "synology", "conf", "resource"))
	if err != nil {
		return err
	}
	sssBytes, err := os.ReadFile(filepath.Join(rootDir, "packaging", "synology", "scripts", "start-stop-status"))
	if err != nil {
		return err
	}
	noopScript := []byte("#!/bin/sh\nexit 0\n")

	// 3. 打包顶层 .spk（未压缩 tar 归档）
	var spkBuf bytes.Buffer
	stw := tar.NewWriter(&spkBuf)
	addTarFile(stw, "INFO", infoBytes, 0o644)
	addTarFile(stw, "PACKAGE_ICON.PNG", icon72, 0o644)
	addTarFile(stw, "PACKAGE_ICON_256.PNG", icon256, 0o644)
	addTarFile(stw, "package.tgz", pkgTgzBytes, 0o644)
	addTarDir(stw, "conf/")
	addTarFile(stw, "conf/privilege", lf(privBytes), 0o644)
	addTarFile(stw, "conf/resource", lf(resBytes), 0o644)
	addTarDir(stw, "scripts/")
	for _, sc := range []string{"preinst", "postinst", "preuninst", "postuninst", "preupgrade", "postupgrade"} {
		addTarFile(stw, "scripts/"+sc, noopScript, 0o755)
	}
	addTarFile(stw, "scripts/start-stop-status", lf(sssBytes), 0o755)
	if err := stw.Close(); err != nil {
		return err
	}
	return os.WriteFile(outPath, spkBuf.Bytes(), 0o644)
}

func lf(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
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

// renderIconPNG 生成带圆角蓝色背景与白色容器方块标识的 PNG 图标。
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
	// 绘制 3x2 白色容器网格块
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
