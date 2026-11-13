//go:build ignore

// build_openwrt.go 纯 Go 跨平台打包生成 OpenWrt / iStoreOS / ImmortalWrt 原生安装包：
//   1. .ipk（适用于 OpenWrt <= 24.10、iStoreOS、ImmortalWrt 的 LuCI「系统 → 软件包 → 上传软件包」或 opkg install）
//   2. .apk（适用于 OpenWrt 25.12+ 的 apk add --allow-untrusted）
//   3. .run（适用于 iStoreOS「iStore 商店 → 手动安装」及全版本 OpenWrt 一键自解压安装）
//
// 用法：go run ./packaging/openwrt/build_openwrt.go
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	pkgVersion = "0.2.0"
	pkgRelease = "1"
)

type targetArch struct {
	goarch    string
	goarm     string
	label     string // 文件名架构后缀
	apkArch   string // OpenWrt 25.12 apk 架构名
	unameGlob string // preinst uname -m 匹配模式
	buildRun  bool
	buildAPK  bool
}

func main() {
	rootDir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	distDir := filepath.Join(rootDir, "dist")
	_ = os.MkdirAll(distDir, 0o755)

	commitBytes, _ := exec.Command("git", "-C", rootDir, "rev-parse", "--short", "HEAD").Output()
	commit := strings.TrimSpace(string(commitBytes))
	if commit == "" {
		commit = "dev"
	}

	targets := []targetArch{
		{
			goarch:    "amd64",
			label:     "x86_64",
			apkArch:   "x86_64",
			unameGlob: "x86_64|amd64",
			buildRun:  true,
			buildAPK:  true,
		},
		{
			goarch:    "arm64",
			label:     "aarch64",
			apkArch:   "aarch64",
			unameGlob: "aarch64*|arm64*",
			buildRun:  true,
			buildAPK:  true,
		},
		{
			goarch:    "arm",
			goarm:     "7",
			label:     "arm",
			apkArch:   "armhf",
			unameGlob: "armv7*|arm*",
			buildRun:  false,
			buildAPK:  false,
		},
	}

	for _, t := range targets {
		if err := buildForTarget(rootDir, distDir, commit, t); err != nil {
			panic(err)
		}
	}
}

type pkgEntry struct {
	path string // 不带前导斜杠，如 "usr/bin/dkstore"
	mode int64
	isDir bool
	data []byte
}

func buildForTarget(rootDir, distDir, commit string, t targetArch) error {
	binBytes, err := compileLinuxBinary(rootDir, commit, t)
	if err != nil {
		return err
	}

	readOW := func(rel ...string) ([]byte, error) {
		p := filepath.Join(append([]string{rootDir, "packaging", "openwrt"}, rel...)...)
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		return stripBOM(lf(b)), nil
	}

	cfgBytes, err := readOW("files", "dkstore.config")
	if err != nil {
		return err
	}
	initBytes, err := readOW("files", "dkstore.init")
	if err != nil {
		return err
	}
	rpcdBytes, err := readOW("luci-app-dkstore", "root", "usr", "libexec", "rpcd", "luci.dkstore")
	if err != nil {
		return err
	}
	aclBytes, err := readOW("luci-app-dkstore", "root", "usr", "share", "rpcd", "acl.d", "luci-app-dkstore.json")
	if err != nil {
		return err
	}
	menuBytes, err := readOW("luci-app-dkstore", "root", "usr", "share", "luci", "menu.d", "luci-app-dkstore.json")
	if err != nil {
		return err
	}
	viewIdxBytes, err := readOW("luci-app-dkstore", "htdocs", "luci-static", "resources", "view", "dkstore", "index.js")
	if err != nil {
		return err
	}
	viewCfgBytes, err := readOW("luci-app-dkstore", "htdocs", "luci-static", "resources", "view", "dkstore", "config.js")
	if err != nil {
		return err
	}

	lmoZhCN := buildLuCILMO(map[string]string{
		"DKStore":          "DKStore 容器更新",
		"Update Center":    "容器更新中心",
		"Service Settings": "服务设置",
	})

	entries := []pkgEntry{
		{path: "etc", isDir: true, mode: 0o755},
		{path: "etc/config", isDir: true, mode: 0o755},
		{path: "etc/config/dkstore", mode: 0o644, data: cfgBytes},
		{path: "etc/init.d", isDir: true, mode: 0o755},
		{path: "etc/init.d/dkstore", mode: 0o755, data: initBytes},
		{path: "usr", isDir: true, mode: 0o755},
		{path: "usr/bin", isDir: true, mode: 0o755},
		{path: "usr/bin/dkstore", mode: 0o755, data: binBytes},
		{path: "usr/lib", isDir: true, mode: 0o755},
		{path: "usr/lib/lua", isDir: true, mode: 0o755},
		{path: "usr/lib/lua/luci", isDir: true, mode: 0o755},
		{path: "usr/lib/lua/luci/i18n", isDir: true, mode: 0o755},
		{path: "usr/lib/lua/luci/i18n/dkstore.zh-cn.lmo", mode: 0o644, data: lmoZhCN},
		{path: "usr/libexec", isDir: true, mode: 0o755},
		{path: "usr/libexec/rpcd", isDir: true, mode: 0o755},
		{path: "usr/libexec/rpcd/luci.dkstore", mode: 0o755, data: rpcdBytes},
		{path: "usr/share", isDir: true, mode: 0o755},
		{path: "usr/share/luci", isDir: true, mode: 0o755},
		{path: "usr/share/luci/menu.d", isDir: true, mode: 0o755},
		{path: "usr/share/luci/menu.d/luci-app-dkstore.json", mode: 0o644, data: menuBytes},
		{path: "usr/share/rpcd", isDir: true, mode: 0o755},
		{path: "usr/share/rpcd/acl.d", isDir: true, mode: 0o755},
		{path: "usr/share/rpcd/acl.d/luci-app-dkstore.json", mode: 0o644, data: aclBytes},
		{path: "www", isDir: true, mode: 0o755},
		{path: "www/luci-static", isDir: true, mode: 0o755},
		{path: "www/luci-static/resources", isDir: true, mode: 0o755},
		{path: "www/luci-static/resources/view", isDir: true, mode: 0o755},
		{path: "www/luci-static/resources/view/dkstore", isDir: true, mode: 0o755},
		{path: "www/luci-static/resources/view/dkstore/index.js", mode: 0o644, data: viewIdxBytes},
		{path: "www/luci-static/resources/view/dkstore/config.js", mode: 0o644, data: viewCfgBytes},
	}

	var installedBytes int64
	for _, e := range entries {
		installedBytes += int64(len(e.data))
	}

	// 1. 生成 .ipk（兼容所有 OpenWrt / iStoreOS / ImmortalWrt opkg 架构）
	ipkName := fmt.Sprintf("luci-app-dkstore_%s-%s_%s.ipk", pkgVersion, pkgRelease, t.label)
	ipkPath := filepath.Join(distDir, ipkName)
	ipkBytes, err := buildIPK(entries, installedBytes, t)
	if err != nil {
		return err
	}
	if err := os.WriteFile(ipkPath, ipkBytes, 0o644); err != nil {
		return err
	}
	fmt.Printf("Built IPK: %s\n", ipkPath)

	// 2. 生成 OpenWrt 25.12+ APKv2 .apk 包
	if t.buildAPK {
		apkName := fmt.Sprintf("luci-app-dkstore-%s-r%s-%s.apk", pkgVersion, pkgRelease, t.label)
		apkPath := filepath.Join(distDir, apkName)
		apkBytes, err := buildAPKv2(entries, installedBytes, t)
		if err != nil {
			return err
		}
		if err := os.WriteFile(apkPath, apkBytes, 0o644); err != nil {
			return err
		}
		fmt.Printf("Built APK: %s\n", apkPath)
	}

	// 3. 生成 iStoreOS / 全版本 OpenWrt 通用自解压安装包 .run
	if t.buildRun {
		runName := fmt.Sprintf("dkstore-openwrt-%s-%s.run", t.label, pkgVersion)
		runPath := filepath.Join(distDir, runName)
		runBytes, err := buildRunInstaller(ipkBytes, entries, t)
		if err != nil {
			return err
		}
		if err := os.WriteFile(runPath, runBytes, 0o755); err != nil {
			return err
		}
		fmt.Printf("Built RUN: %s\n", runPath)
	}

	return nil
}

func compileLinuxBinary(rootDir, commit string, t targetArch) ([]byte, error) {
	tmpBin := filepath.Join(os.TempDir(), fmt.Sprintf("dkstore-owrt-%s-%d", t.label, os.Getpid()))
	defer os.Remove(tmpBin)

	ldflags := fmt.Sprintf("-s -w -X github.com/kukushouhou/DKStore/internal/version.Version=%s -X github.com/kukushouhou/DKStore/internal/version.Commit=%s", pkgVersion, commit)
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags="+ldflags, "-o", tmpBin, "./cmd/dkstore")
	cmd.Dir = rootDir
	env := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+t.goarch)
	if t.goarm != "" {
		env = append(env, "GOARM="+t.goarm)
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go build (%s) failed: %v\n%s", t.label, err, string(out))
	}
	return os.ReadFile(tmpBin)
}

func preinstScript(t targetArch) []byte {
	return []byte(fmt.Sprintf(`#!/bin/sh
if [ -z "${IPKG_INSTROOT}" ]; then
	ARCH="$(uname -m 2>/dev/null || true)"
	case "$ARCH" in
		%s) ;;
		"") ;;
		*)
			echo "Error: CPU architecture mismatch! Package is built for %s, but router is $ARCH." >&2
			echo "错误：安装包架构不匹配！当前安装包适用于 %s，但当前设备架构为 $ARCH。" >&2
			exit 1
			;;
	esac
fi
exit 0
`, t.unameGlob, t.label, t.label))
}

func postinstScript() []byte {
	return []byte(`#!/bin/sh
if [ -z "${IPKG_INSTROOT}" ]; then
	chmod 755 /usr/bin/dkstore /etc/init.d/dkstore /usr/libexec/rpcd/luci.dkstore 2>/dev/null || true
	/etc/init.d/dkstore enable >/dev/null 2>&1 || true
	/etc/init.d/dkstore restart >/dev/null 2>&1 || true
	/etc/init.d/rpcd reload >/dev/null 2>&1 || true
	/etc/init.d/uhttpd reload >/dev/null 2>&1 || true
	rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache/* 2>/dev/null || true
fi
exit 0
`)
}

func prermScript() []byte {
	return []byte(`#!/bin/sh
if [ -z "${IPKG_INSTROOT}" ]; then
	case "$1" in
		upgrade)
			;;
		*)
			/etc/init.d/dkstore stop >/dev/null 2>&1 || true
			/etc/init.d/dkstore disable >/dev/null 2>&1 || true
			;;
	esac
fi
exit 0
`)
}

// buildIPK 生成符合 OpenWrt ipkg-build 规范的 .ipk（外层 tar.gz 含 ./debian-binary、./control.tar.gz、./data.tar.gz）。
// Architecture 设为 all 并在 preinst 中校验 uname -m，从而同时兼容 aarch64_generic / aarch64_cortex-a53 / aarch64_cortex-a72 等全部细分架构。
func buildIPK(entries []pkgEntry, installedSize int64, t targetArch) ([]byte, error) {
	controlText := strings.Join([]string{
		"Package: luci-app-dkstore",
		fmt.Sprintf("Version: %s-%s", pkgVersion, pkgRelease),
		"Depends: libc",
		"Provides: dkstore",
		"Section: luci",
		"Category: LuCI",
		"Maintainer: kukushouhou <https://github.com/kukushouhou/DKStore>",
		"Architecture: all",
		fmt.Sprintf("Installed-Size: %d", installedSize),
		fmt.Sprintf("Description: DKStore Cross-Platform Docker App Update Center (%s, with LuCI integration, service-level upgrade & rollback).", t.label),
		"",
	}, "\n")

	// 1. data.tar.gz
	dataTgz, err := buildTarGz(func(tw *tar.Writer) error {
		writeUSTARDir(tw, "./", 0o755)
		for _, e := range entries {
			name := "./" + strings.TrimPrefix(e.path, "/")
			if e.isDir {
				if !strings.HasSuffix(name, "/") {
					name += "/"
				}
				writeUSTARDir(tw, name, e.mode)
			} else {
				writeUSTARFile(tw, name, e.data, e.mode)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 2. control.tar.gz
	ctrlTgz, err := buildTarGz(func(tw *tar.Writer) error {
		writeUSTARDir(tw, "./", 0o755)
		writeUSTARFile(tw, "./control", []byte(controlText), 0o644)
		writeUSTARFile(tw, "./conffiles", []byte("/etc/config/dkstore\n"), 0o644)
		writeUSTARFile(tw, "./preinst", preinstScript(t), 0o755)
		writeUSTARFile(tw, "./postinst", postinstScript(), 0o755)
		writeUSTARFile(tw, "./prerm", prermScript(), 0o755)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 3. 外层 .ipk（tar.gz）
	return buildTarGz(func(tw *tar.Writer) error {
		writeUSTARDir(tw, "./", 0o755)
		writeUSTARFile(tw, "./debian-binary", []byte("2.0\n"), 0o644)
		writeUSTARFile(tw, "./control.tar.gz", ctrlTgz, 0o644)
		writeUSTARFile(tw, "./data.tar.gz", dataTgz, 0o644)
		return nil
	})
}

// buildAPKv2 生成符合 Alpine / OpenWrt 25.12+ APKv2 规范的 .apk 归档（控制流 gzip [无 tar 尾部零块] + 数据流 gzip）。
func buildAPKv2(entries []pkgEntry, installedSize int64, t targetArch) ([]byte, error) {
	// 1. 构建数据段 tar.gz（完整包含 1024B tar 结束零块）
	var rawDataTar bytes.Buffer
	dtw := tar.NewWriter(&rawDataTar)
	for _, e := range entries {
		name := strings.TrimPrefix(e.path, "/")
		if e.isDir {
			if !strings.HasSuffix(name, "/") {
				name += "/"
			}
			writeAPKRecord(dtw, name, nil, e.mode, tar.TypeDir)
		} else {
			writeAPKRecord(dtw, name, e.data, e.mode, tar.TypeReg)
		}
	}
	if err := dtw.Close(); err != nil {
		return nil, err
	}
	var dataGzBuf bytes.Buffer
	dgw := gzip.NewWriter(&dataGzBuf)
	_, _ = dgw.Write(rawDataTar.Bytes())
	if err := dgw.Close(); err != nil {
		return nil, err
	}
	dataGzBytes := dataGzBuf.Bytes()
	dataHash := sha256.Sum256(dataGzBytes)

	// 2. 构建控制段 tar（去掉末尾两个 512B 全零 EOF 块，符合 APKv2 多流拼接规范）
	nowUnix := time.Now().Unix()
	pkgInfo := strings.Join([]string{
		"# Generated by DKStore build_openwrt.go",
		"pkgname = luci-app-dkstore",
		fmt.Sprintf("pkgver = %s-r%s", pkgVersion, pkgRelease),
		"pkgdesc = DKStore Cross-Platform Docker App Update Center for OpenWrt",
		"url = https://github.com/kukushouhou/DKStore",
		fmt.Sprintf("builddate = %d", nowUnix),
		"packager = kukushouhou",
		fmt.Sprintf("size = %d", installedSize),
		fmt.Sprintf("arch = %s", t.apkArch),
		"origin = luci-app-dkstore",
		"maintainer = kukushouhou",
		"license = Apache-2.0",
		"provides = dkstore",
		fmt.Sprintf("datahash = %s", hex.EncodeToString(dataHash[:])),
		"",
	}, "\n")

	var rawCtrlTar bytes.Buffer
	ctw := tar.NewWriter(&rawCtrlTar)
	writeAPKRecord(ctw, ".PKGINFO", []byte(pkgInfo), 0o644, tar.TypeReg)
	writeAPKRecord(ctw, ".pre-install", preinstScript(t), 0o755, tar.TypeReg)
	writeAPKRecord(ctw, ".post-install", postinstScript(), 0o755, tar.TypeReg)
	writeAPKRecord(ctw, ".pre-deinstall", prermScript(), 0o755, tar.TypeReg)
	if err := ctw.Close(); err != nil {
		return nil, err
	}
	ctrlTarBytes := trimTarEOF(rawCtrlTar.Bytes())

	var ctrlGzBuf bytes.Buffer
	cgw := gzip.NewWriter(&ctrlGzBuf)
	_, _ = cgw.Write(ctrlTarBytes)
	if err := cgw.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	out.Write(ctrlGzBuf.Bytes())
	out.Write(dataGzBytes)
	return out.Bytes(), nil
}

// buildRunInstaller 生成兼容 iStoreOS「手动安装 (.run)」及全版本 OpenWrt（opkg / apk）的一键自解压安装包。
// 采用 4096 字节块对齐的纯 POSIX dd 提取内嵌 .ipk（零 base64 依赖，兼容裁剪版 BusyBox，体积仅 ~6.5 MB）。
func buildRunInstaller(ipkBytes []byte, _ []pkgEntry, t targetArch) ([]byte, error) {
	const blockSize = 4096
	fullBlocks := len(ipkBytes) / blockSize
	remBytes := len(ipkBytes) % blockSize

	makeHeader := func(hdrBlocks int) string {
		remSkip := hdrBlocks*blockSize + fullBlocks*blockSize
		return fmt.Sprintf(`#!/bin/sh
set -e

echo "==> Installing DKStore %s for OpenWrt / iStoreOS (%s)..."

ARCH="$(uname -m 2>/dev/null || true)"
case "$ARCH" in
	%s|"") ;;
	*)
		echo "Error: Package is built for %s, current device is $ARCH." >&2
		exit 1
		;;
esac

TMP_DIR="/tmp/dkstore-install.$$"
mkdir -p "$TMP_DIR"
cleanup() {
	rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

IPK_FILE="$TMP_DIR/luci-app-dkstore.ipk"
dd if="$0" bs=%d skip=%d count=%d of="$IPK_FILE" 2>/dev/null
if [ %d -gt 0 ]; then
	dd if="$0" bs=1 skip=%d count=%d >> "$IPK_FILE" 2>/dev/null
fi

if command -v opkg >/dev/null 2>&1; then
	echo "==> Detected opkg package manager, installing luci-app-dkstore.ipk..."
	opkg install --force-reinstall "$IPK_FILE"
else
	echo "==> Installing DKStore payload directly (OpenWrt apk / rootfs mode)..."
	if [ -f /etc/config/dkstore ]; then
		cp -f /etc/config/dkstore "$TMP_DIR/dkstore.config.bak"
	fi
	tar -xzOf "$IPK_FILE" ./data.tar.gz | tar -xzf - -C /
	if [ -f "$TMP_DIR/dkstore.config.bak" ]; then
		cp -f "$TMP_DIR/dkstore.config.bak" /etc/config/dkstore
	fi
	chmod 755 /usr/bin/dkstore /etc/init.d/dkstore /usr/libexec/rpcd/luci.dkstore
	/etc/init.d/dkstore enable >/dev/null 2>&1 || true
	/etc/init.d/dkstore restart >/dev/null 2>&1 || true
	/etc/init.d/rpcd reload >/dev/null 2>&1 || true
	/etc/init.d/uhttpd reload >/dev/null 2>&1 || true
	rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache/* 2>/dev/null || true
fi

echo "==> DKStore installed and started! Open LuCI -> Services -> DKStore."
exit 0
`, pkgVersion, t.label, t.unameGlob, t.label, blockSize, hdrBlocks, fullBlocks, remBytes, remSkip, remBytes)
	}

	hdrBlocks := 1
	var hdr string
	for {
		hdr = makeHeader(hdrBlocks) + "# __PAYLOAD_PADDING__\n"
		if len(hdr) <= hdrBlocks*blockSize {
			break
		}
		hdrBlocks++
	}
	padLen := hdrBlocks*blockSize - len(hdr)
	var out bytes.Buffer
	out.WriteString(hdr)
	if padLen > 0 {
		if padLen > 1 {
			out.Write(bytes.Repeat([]byte("#"), padLen-1))
		}
		out.WriteByte('\n')
	}
	out.Write(ipkBytes)
	return out.Bytes(), nil
}

func buildTarGz(fn func(tw *tar.Writer) error) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := fn(tw); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeUSTARDir(tw *tar.Writer, name string, mode int64) {
	_ = tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     mode,
		Uid:      0,
		Gid:      0,
		Uname:    "root",
		Gname:    "root",
		Typeflag: tar.TypeDir,
		ModTime:  time.Unix(1700000000, 0),
		Format:   tar.FormatUSTAR,
	})
}

func writeUSTARFile(tw *tar.Writer, name string, data []byte, mode int64) {
	_ = tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(data)),
		Uid:      0,
		Gid:      0,
		Uname:    "root",
		Gname:    "root",
		Typeflag: tar.TypeReg,
		ModTime:  time.Unix(1700000000, 0),
		Format:   tar.FormatUSTAR,
	})
	_, _ = tw.Write(data)
}

func writeAPKRecord(tw *tar.Writer, name string, data []byte, mode int64, typeflag byte) {
	sum := sha1.Sum(data)
	hdr := &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(data)),
		Uid:      0,
		Gid:      0,
		Uname:    "root",
		Gname:    "root",
		Typeflag: typeflag,
		ModTime:  time.Unix(1700000000, 0),
		Format:   tar.FormatPAX,
		PAXRecords: map[string]string{
			"APK-TOOLS.checksum.SHA1": hex.EncodeToString(sum[:]),
		},
	}
	_ = tw.WriteHeader(hdr)
	if len(data) > 0 {
		_, _ = tw.Write(data)
	}
}

func trimTarEOF(b []byte) []byte {
	for len(b) >= 512 {
		tail := b[len(b)-512:]
		allZero := true
		for _, v := range tail {
			if v != 0 {
				allZero = false
				break
			}
		}
		if !allZero {
			break
		}
		b = b[:len(b)-512]
	}
	return b
}

// buildLuCILMO 生成 OpenWrt LuCI 原生二进制翻译字典（po2lmo 格式）。
func buildLuCILMO(dict map[string]string) []byte {
	type lmoEntry struct {
		keyHash uint32
		valHash uint32
		offset  uint32
		length  uint32
	}
	keys := make([]string, 0, len(dict))
	for k := range dict {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var body bytes.Buffer
	entries := make([]lmoEntry, 0, len(keys))
	for _, k := range keys {
		v := dict[k]
		if v == "" {
			continue
		}
		kh := sfhHash([]byte(k))
		vh := sfhHash([]byte(v))
		if kh == vh {
			continue
		}
		off := uint32(body.Len())
		vb := []byte(v)
		body.Write(vb)
		if rem := len(vb) % 4; rem != 0 {
			body.Write(make([]byte, 4-rem))
		}
		entries = append(entries, lmoEntry{
			keyHash: kh,
			valHash: vh,
			offset:  off,
			length:  uint32(len(vb)),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].keyHash < entries[j].keyHash
	})

	indexOffset := uint32(body.Len())
	for _, e := range entries {
		_ = binary.Write(&body, binary.BigEndian, e.keyHash)
		_ = binary.Write(&body, binary.BigEndian, e.valHash)
		_ = binary.Write(&body, binary.BigEndian, e.offset)
		_ = binary.Write(&body, binary.BigEndian, e.length)
	}
	_ = binary.Write(&body, binary.BigEndian, indexOffset)
	return body.Bytes()
}

// sfhHash 实现 Paul Hsieh SuperFastHash（与 OpenWrt luci-base lmo_core.c 完全一致）。
func sfhHash(data []byte) uint32 {
	n := len(data)
	if n == 0 {
		return 0
	}
	hash := uint32(n)
	rem := n & 3
	loops := n >> 2
	idx := 0
	for ; loops > 0; loops-- {
		hash += uint32(uint16(data[idx]) | uint16(data[idx+1])<<8)
		tmp := (uint32(uint16(data[idx+2])|uint16(data[idx+3])<<8) << 11) ^ hash
		hash = (hash << 16) ^ tmp
		hash += hash >> 11
		idx += 4
	}
	switch rem {
	case 3:
		hash += uint32(uint16(data[idx]) | uint16(data[idx+1])<<8)
		hash ^= hash << 16
		hash ^= uint32(int32(int8(data[idx+2]))) << 18
		hash += hash >> 11
	case 2:
		hash += uint32(uint16(data[idx]) | uint16(data[idx+1])<<8)
		hash ^= hash << 11
		hash += hash >> 17
	case 1:
		hash += uint32(int32(int8(data[idx])))
		hash ^= hash << 10
		hash += hash >> 1
	}
	hash ^= hash << 3
	hash += hash >> 5
	hash ^= hash << 4
	hash += hash >> 17
	hash ^= hash << 25
	hash += hash >> 6
	return hash
}

func lf(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func stripBOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
}
