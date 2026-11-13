//go:build ignore

// build_fpk.go 打包生成飞牛 fnOS 可直接手动安装的 .fpk 应用包（兼容 fnpack 1.2.3 规范，无需依赖 Linux 环境）。
// 用法：go run ./tools/build_fpk.go [-version 0.2.0] [-bin-dir <optional_prebuilt_dir>]
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	verFlag := flag.String("version", "", "版本号（如 0.2.0 或 v0.2.0）")
	binDirFlag := flag.String("bin-dir", "", "可选预编译二进制目录（含 dkstore-linux-amd64 / dkstore-linux-arm64）")
	flag.Parse()

	ver := normalizeVer(*verFlag)
	rootDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	distDir := filepath.Join(rootDir, "dist")
	_ = os.MkdirAll(distDir, 0o755)
	commit := gitShortCommit(rootDir)

	targets := []struct {
		goarch   string
		platform string
		fpkName  string
	}{
		{
			goarch:   "amd64",
			platform: "x86",
			fpkName:  fmt.Sprintf("dkstore-fnos-x86-%s.fpk", ver),
		},
		{
			goarch:   "arm64",
			platform: "arm",
			fpkName:  fmt.Sprintf("dkstore-fnos-arm-%s.fpk", ver),
		},
	}

	for _, t := range targets {
		fpkPath := filepath.Join(distDir, t.fpkName)
		if err := buildFPK(rootDir, fpkPath, ver, commit, t.goarch, t.platform, *binDirFlag); err != nil {
			panic(err)
		}
		fmt.Printf("Built FPK: %s\n", fpkPath)
	}
}

func normalizeVer(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		v = strings.TrimSpace(os.Getenv("DKSTORE_VERSION"))
	}
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		v = "0.2.0"
	}
	return v
}

func gitShortCommit(rootDir string) string {
	out, _ := exec.Command("git", "-C", rootDir, "rev-parse", "--short", "HEAD").Output()
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "dev"
}

func loadOrBuildLinuxBin(rootDir, ver, commit, goarch, binDir string) ([]byte, error) {
	if binDir != "" {
		cand := filepath.Join(binDir, "dkstore-linux-"+goarch)
		if b, err := os.ReadFile(cand); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	tmpBin := filepath.Join(os.TempDir(), fmt.Sprintf("dkstore-fnos-%s-%d", goarch, os.Getpid()))
	defer os.Remove(tmpBin)

	ldflags := fmt.Sprintf("-s -w -X github.com/kukushouhou/DKStore/internal/version.Version=v%s -X github.com/kukushouhou/DKStore/internal/version.Commit=%s", ver, commit)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags="+ldflags, "-o", tmpBin, "./cmd/dkstore")
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build (%s) failed: %v\n%s", goarch, err, string(out))
	}
	return os.ReadFile(tmpBin)
}

func buildFPK(rootDir, outPath, ver, commit, goarch, fnPlatform, binDir string) error {
	binBytes, err := loadOrBuildLinuxBin(rootDir, ver, commit, goarch, binDir)
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

	manifestLines := []string{
		"appname               = dkstore",
		fmt.Sprintf("version               = %s", ver),
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
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	scale := 64.0 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var rSum, gSum, bSum, aSum float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(px) + (float64(sx)+0.5)/float64(ss)) * scale
					v := (float64(py) + (float64(sy)+0.5)/float64(ss)) * scale
					c := sampleF23Logo(u, v)
					af := float64(c.A) / 255.0
					rSum += float64(c.R) * af
					gSum += float64(c.G) * af
					bSum += float64(c.B) * af
					aSum += af
				}
			}
			if aSum > 0 {
				inv := 1.0 / aSum
				aOut := uint8(math.Round((aSum / float64(ss*ss)) * 255.0))
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(math.Round(rSum * inv)),
					G: uint8(math.Round(gSum * inv)),
					B: uint8(math.Round(bSum * inv)),
					A: aOut,
				})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func sampleF23Logo(u, v float64) color.NRGBA {
	if !inRoundBox(u, v, 2, 2, 62, 62, 15) {
		return color.NRGBA{}
	}
	if inRoundBox(u, v, 29.2, 34.5, 34.8, 42.0, 2.8) {
		return color.NRGBA{R: 103, G: 232, B: 249, A: 255}
	}
	if distSeg(u, v, 20, 13, 44, 13) <= 2.2 ||
		distSeg(u, v, 13, 28.5, 32, 14) <= 2.5 ||
		distSeg(u, v, 32, 14, 51, 28.5) <= 2.5 {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	inOuterD := (v >= 41 && inRoundBox(u, v, 17, 38, 47, 50.5, 3.0)) ||
		(v <= 41 && v >= 25 && sq((u-32)/15.0)+sq((v-41)/16.0) <= 1.0)
	inInnerD := (v >= 41 && v <= 45.2 && u >= 22.4 && u <= 41.6) ||
		(v <= 41 && v >= 30.5 && sq((u-32)/9.6)+sq((v-41)/10.5) <= 1.0)
	if inOuterD && !inInnerD {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	t := ((u-8)*48 + (v-4)*56) / (48*48 + 56*56)
	t = max(0, min(1, t))
	if t < 0.5 {
		k := t * 2
		return lerpColor(color.NRGBA{79, 140, 255, 255}, color.NRGBA{47, 107, 255, 255}, k)
	}
	k := (t - 0.5) * 2
	return lerpColor(color.NRGBA{47, 107, 255, 255}, color.NRGBA{21, 60, 192, 255}, k)
}

func sq(x float64) float64 { return x * x }

func lerpColor(a, b color.NRGBA, t float64) color.NRGBA {
	return color.NRGBA{
		R: uint8(math.Round(float64(a.R) + (float64(b.R)-float64(a.R))*t)),
		G: uint8(math.Round(float64(a.G) + (float64(b.G)-float64(a.G))*t)),
		B: uint8(math.Round(float64(a.B) + (float64(b.B)-float64(a.B))*t)),
		A: 255,
	}
}

func distSeg(px, py, x1, y1, x2, y2 float64) float64 {
	dx, dy := x2-x1, y2-y1
	t := ((px-x1)*dx + (py-y1)*dy) / (dx*dx + dy*dy)
	t = max(0, min(1, t))
	cx, cy := x1+t*dx, y1+t*dy
	return math.Hypot(px-cx, py-cy)
}

func inRoundBox(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx := max(x0+r, min(x1-r, px))
	cy := max(y0+r, min(y1-r, py))
	return sq(px-cx)+sq(py-cy) <= r*r
}
