//go:build ignore

// build_release.go 一键构建全部发行产物（群晖 .spk、飞牛 .fpk、OpenWrt .ipk/.apk/.run、Linux/Windows 独立归档包与 SHA256SUMS.txt）。
// 用法：go run ./tools/build_release.go [-version v0.2.0]
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	verFlag := flag.String("version", "", "发布版本号（如 v0.2.0 或 0.2.0）")
	cleanFlag := flag.Bool("clean", true, "构建前清空 dist 目录")
	flag.Parse()

	ver := strings.TrimPrefix(strings.TrimSpace(*verFlag), "v")
	if ver == "" {
		ver = strings.TrimPrefix(strings.TrimSpace(os.Getenv("DKSTORE_VERSION")), "v")
	}
	if ver == "" {
		ver = "0.2.0"
	}

	rootDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	distDir := filepath.Join(rootDir, "dist")
	if *cleanFlag {
		_ = os.RemoveAll(distDir)
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		panic(err)
	}

	commitBytes, _ := exec.Command("git", "-C", rootDir, "rev-parse", "--short", "HEAD").Output()
	commit := strings.TrimSpace(string(commitBytes))
	if commit == "" {
		commit = "dev"
	}

	binCacheDir, err := os.MkdirTemp("", "dkstore-release-bins-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(binCacheDir)

	// 1. 一次性交叉编译各平台核心二进制，供下方各打包器直接复用
	builds := []struct {
		key    string
		goos   string
		goarch string
		goarm  string
		outBin string
	}{
		{key: "linux-amd64", goos: "linux", goarch: "amd64", outBin: "dkstore-linux-amd64"},
		{key: "linux-arm64", goos: "linux", goarch: "arm64", outBin: "dkstore-linux-arm64"},
		{key: "linux-armv7", goos: "linux", goarch: "arm", goarm: "7", outBin: "dkstore-linux-armv7"},
		{key: "windows-amd64", goos: "windows", goarch: "amd64", outBin: "dkstore-windows-amd64.exe"},
	}

	ldflags := fmt.Sprintf("-s -w -X github.com/kukushouhou/DKStore/internal/version.Version=v%s -X github.com/kukushouhou/DKStore/internal/version.Commit=%s", ver, commit)
	for _, b := range builds {
		fmt.Printf("==> Compiling %s (v%s, %s)...\n", b.key, ver, commit)
		outPath := filepath.Join(binCacheDir, b.outBin)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags="+ldflags, "-o", outPath, "./cmd/dkstore")
		cmd.Dir = rootDir
		env := append(os.Environ(), "CGO_ENABLED=0", "GOOS="+b.goos, "GOARCH="+b.goarch)
		if b.goarm != "" {
			env = append(env, "GOARM="+b.goarm)
		}
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			panic(fmt.Sprintf("compile %s failed: %v\n%s", b.key, err, string(out)))
		}
	}

	// 2. 调用三大平台原生套件打包器（直接复用预编译二进制）
	for _, script := range []string{"./tools/build_spk.go", "./tools/build_fpk.go", "./tools/build_openwrt.go"} {
		cmd := exec.Command("go", "run", script, "-version", ver, "-bin-dir", binCacheDir)
		cmd.Dir = rootDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			panic(fmt.Sprintf("%s failed: %v", script, err))
		}
	}

	// 3. 生成通用独立二进制归档包（Linux systemd .tar.gz + Windows .zip）
	readmeEN, _ := os.ReadFile(filepath.Join(rootDir, "README.md"))
	readmeZH, _ := os.ReadFile(filepath.Join(rootDir, "README_zh-CN.md"))
	systemdUnit, _ := os.ReadFile(filepath.Join(rootDir, "packaging", "linux", "dkstore.service"))

	for _, archKey := range []string{"amd64", "arm64", "armv7"} {
		binBytes, err := os.ReadFile(filepath.Join(binCacheDir, "dkstore-linux-"+archKey))
		if err != nil {
			panic(err)
		}
		tgzName := fmt.Sprintf("dkstore-linux-%s-%s.tar.gz", archKey, ver)
		tgzPath := filepath.Join(distDir, tgzName)
		if err := writeLinuxTarGz(tgzPath, binBytes, systemdUnit, readmeEN, readmeZH); err != nil {
			panic(err)
		}
		fmt.Printf("Built Archive: %s\n", tgzPath)
	}

	winBin, err := os.ReadFile(filepath.Join(binCacheDir, "dkstore-windows-amd64.exe"))
	if err != nil {
		panic(err)
	}
	zipPath := filepath.Join(distDir, fmt.Sprintf("dkstore-windows-amd64-%s.zip", ver))
	if err := writeWindowsZip(zipPath, winBin, readmeEN, readmeZH); err != nil {
		panic(err)
	}
	fmt.Printf("Built Archive: %s\n", zipPath)

	// 4. 生成 SHA256SUMS.txt
	if err := writeSHA256Sums(distDir); err != nil {
		panic(err)
	}
	fmt.Printf("Built Checksums: %s\n", filepath.Join(distDir, "SHA256SUMS.txt"))
}

func writeLinuxTarGz(outPath string, binBytes, serviceBytes, readmeEN, readmeZH []byte) error {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	writeFile := func(name string, data []byte, mode int64) {
		if len(data) == 0 {
			return
		}
		_ = tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     mode,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
			ModTime:  time.Unix(1700000000, 0),
		})
		_, _ = tw.Write(data)
	}
	writeFile("dkstore", binBytes, 0o755)
	writeFile("dkstore.service", bytes.ReplaceAll(serviceBytes, []byte("\r\n"), []byte("\n")), 0o644)
	writeFile("README.md", readmeEN, 0o644)
	writeFile("README_zh-CN.md", readmeZH, 0o644)

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

func writeWindowsZip(outPath string, exeBytes, readmeEN, readmeZH []byte) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	addZip := func(name string, data []byte) error {
		if len(data) == 0 {
			return nil
		}
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := addZip("dkstore.exe", exeBytes); err != nil {
		return err
	}
	if err := addZip("README.md", readmeEN); err != nil {
		return err
	}
	if err := addZip("README_zh-CN.md", readmeZH); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

func writeSHA256Sums(distDir string) error {
	entries, err := os.ReadDir(distDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == "SHA256SUMS.txt" || e.Name() == "RELEASE_NOTES.md" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var lines []string
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(distDir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		lines = append(lines, fmt.Sprintf("%s  %s", hex.EncodeToString(sum[:]), name))
	}
	return os.WriteFile(filepath.Join(distDir, "SHA256SUMS.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
