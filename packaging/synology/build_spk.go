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
	"math"
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

// renderIconPNG 按 F2-3 品牌 Logo（飞檐 K 冠 + 拱门 D 屋 + 青芯）渲染抗锯齿 PNG 图标。
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
	// 青色发光核心
	if inRoundBox(u, v, 29.2, 34.5, 34.8, 42.0, 2.8) {
		return color.NRGBA{R: 103, G: 232, B: 249, A: 255}
	}
	// 白色 K 冠（顶梁 + 屋檐斜臂）
	if distSeg(u, v, 20, 13, 44, 13) <= 2.2 ||
		distSeg(u, v, 13, 28.5, 32, 14) <= 2.5 ||
		distSeg(u, v, 32, 14, 51, 28.5) <= 2.5 {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	// 白色拱门 D 屋（外拱减去内拱）
	inOuterD := (v >= 41 && inRoundBox(u, v, 17, 38, 47, 50.5, 3.0)) ||
		(v <= 41 && v >= 25 && sq((u-32)/15.0)+sq((v-41)/16.0) <= 1.0)
	inInnerD := (v >= 41 && v <= 45.2 && u >= 22.4 && u <= 41.6) ||
		(v <= 41 && v >= 30.5 && sq((u-32)/9.6)+sq((v-41)/10.5) <= 1.0)
	if inOuterD && !inInnerD {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	}
	// 对角线蓝渐变底色 (#4F8CFF -> #2F6BFF -> #153CC0)
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
