package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

const maxExtractedBinaryBytes = 160 << 20 // 160 MB 安全上限，防止异常大包耗尽内存

var (
	ErrUnsupportedPackage = errors.New("unsupported update package format")
	ErrArchMismatch       = errors.New("package CPU architecture or OS does not match current host")
)

// ExtractBinaryFromFile 从下载或上传的文件（支持裸二进制、.tar.gz/.tgz、.zip、.gz、.spk、.fpk、.ipk、.apk）中提取 dkstore 主程序。
func ExtractBinaryFromFile(filePath, origName string) ([]byte, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return extractBinaryFromBytes(data, origName, 0)
}

func extractBinaryFromBytes(data []byte, origName string, depth int) ([]byte, error) {
	if depth > 3 || len(data) < 4 {
		return nil, ErrUnsupportedPackage
	}

	// 1. 裸 ELF 或 Windows PE 可执行文件
	if isELF(data) || isPE(data) {
		return data, nil
	}

	// 2. ZIP 归档（如 dkstore-windows-amd64-*.zip）
	if bytes.HasPrefix(data, []byte{'P', 'K', 0x03, 0x04}) {
		return extractFromZip(data)
	}

	// 3. Gzip 压缩流（单文件 .gz 或 .tar.gz / .tgz / .apk / tar-ipk）
	if data[0] == 0x1f && data[1] == 0x8b {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, ErrUnsupportedPackage
		}
		defer gr.Close()
		uncompressed, err := io.ReadAll(io.LimitReader(gr, maxExtractedBinaryBytes))
		if err != nil {
			return nil, ErrUnsupportedPackage
		}
		if isELF(uncompressed) || isPE(uncompressed) {
			return uncompressed, nil
		}
		if out, err := extractFromTar(uncompressed, depth+1); err == nil {
			return out, nil
		}
		return nil, ErrUnsupportedPackage
	}

	// 4. Debian ar 归档（标准 OpenWrt .ipk）
	if bytes.HasPrefix(data, []byte("!<arch>\n")) {
		if out, err := extractFromAr(data, depth+1); err == nil {
			return out, nil
		}
	}

	// 5. 未压缩 Tar 归档（如群晖 .spk、飞牛 .fpk 外层 tar）
	if out, err := extractFromTar(data, depth+1); err == nil {
		return out, nil
	}

	_ = origName
	return nil, ErrUnsupportedPackage
}

func extractFromZip(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrUnsupportedPackage
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		base := strings.ToLower(path.Base(strings.ReplaceAll(f.Name, "\\", "/")))
		if base == "dkstore" || base == "dkstore.exe" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(io.LimitReader(rc, maxExtractedBinaryBytes))
			rc.Close()
			if err != nil {
				return nil, err
			}
			if isELF(b) || isPE(b) {
				return b, nil
			}
		}
	}
	return nil, ErrUnsupportedPackage
}

func extractFromTar(data []byte, depth int) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(data))
	var nestedArchives [][]byte
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(hdr.Name, "\\", "/")), "./")
		base := strings.ToLower(path.Base(clean))
		if base == "dkstore" || base == "dkstore.exe" {
			b, err := io.ReadAll(io.LimitReader(tr, maxExtractedBinaryBytes))
			if err != nil {
				return nil, err
			}
			if isELF(b) || isPE(b) {
				return b, nil
			}
		}
		if base == "package.tgz" || base == "app.tgz" || base == "data.tar.gz" {
			b, err := io.ReadAll(io.LimitReader(tr, maxExtractedBinaryBytes))
			if err == nil && len(b) > 0 {
				nestedArchives = append(nestedArchives, b)
			}
		}
	}
	for _, nested := range nestedArchives {
		if out, err := extractBinaryFromBytes(nested, "", depth+1); err == nil {
			return out, nil
		}
	}
	return nil, ErrUnsupportedPackage
}

func extractFromAr(data []byte, depth int) ([]byte, error) {
	pos := 8 // skip "!<arch>\n"
	for pos+60 <= len(data) {
		hdr := data[pos : pos+60]
		if hdr[58] != 0x60 || hdr[59] != 0x0a {
			break
		}
		name := strings.TrimRight(strings.TrimSpace(string(hdr[0:16])), "/")
		sizeStr := strings.TrimSpace(string(hdr[48:58]))
		sz, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil || sz < 0 {
			break
		}
		bodyStart := pos + 60
		bodyEnd := bodyStart + int(sz)
		if bodyEnd > len(data) {
			break
		}
		if strings.HasPrefix(name, "data.tar") {
			return extractBinaryFromBytes(data[bodyStart:bodyEnd], name, depth+1)
		}
		pos = bodyEnd
		if pos%2 == 1 {
			pos++
		}
	}
	return nil, ErrUnsupportedPackage
}

func isELF(b []byte) bool {
	return len(b) >= 20 && b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F'
}

func isPE(b []byte) bool {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return false
	}
	peOff := int(binary.LittleEndian.Uint32(b[0x3c:0x40]))
	if peOff < 0 || peOff+6 > len(b) {
		return false
	}
	return b[peOff] == 'P' && b[peOff+1] == 'E' && b[peOff+2] == 0 && b[peOff+3] == 0
}

func verifyBinaryFileArch(path, goos, goarch string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return verifyBinaryArch(buf[:n], goos, goarch)
}

func verifyBinaryArch(b []byte, goos, goarch string) error {
	switch goos {
	case "linux":
		if !isELF(b) {
			return fmt.Errorf("%w: expected Linux ELF binary", ErrArchMismatch)
		}
		elfClass := b[4] // 1 = 32-bit, 2 = 64-bit
		var machine uint16
		if b[5] == 2 {
			machine = binary.BigEndian.Uint16(b[18:20])
		} else {
			machine = binary.LittleEndian.Uint16(b[18:20])
		}
		switch goarch {
		case "amd64":
			if elfClass != 2 || machine != 62 {
				return fmt.Errorf("%w: expected linux/amd64", ErrArchMismatch)
			}
		case "arm64":
			if elfClass != 2 || machine != 183 {
				return fmt.Errorf("%w: expected linux/arm64", ErrArchMismatch)
			}
		case "arm":
			if elfClass != 1 || machine != 40 {
				return fmt.Errorf("%w: expected linux/arm", ErrArchMismatch)
			}
		}
		return nil
	case "windows":
		if !isPE(b) {
			return fmt.Errorf("%w: expected Windows PE executable", ErrArchMismatch)
		}
		peOff := int(binary.LittleEndian.Uint32(b[0x3c:0x40]))
		machine := binary.LittleEndian.Uint16(b[peOff+4 : peOff+6])
		switch goarch {
		case "amd64":
			if machine != 0x8664 {
				return fmt.Errorf("%w: expected windows/amd64", ErrArchMismatch)
			}
		case "arm64":
			if machine != 0xAA64 {
				return fmt.Errorf("%w: expected windows/arm64", ErrArchMismatch)
			}
		}
		return nil
	default:
		return nil
	}
}
