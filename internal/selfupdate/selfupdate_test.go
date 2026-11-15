package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

func TestConfigNormalizeAndEffectiveURL(t *testing.T) {
	cfg, err := NormalizeAndValidate(Config{
		Repo:    "https://github.com/myfork/DKStore.git",
		Channel: ChannelGitHub,
	}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Repo != "myfork/DKStore" || cfg.Policy != PolicyAuto {
		t.Fatalf("got repo=%q policy=%q, want myfork/DKStore + auto", cfg.Repo, cfg.Policy)
	}
	if got := EffectiveBaseURL(cfg); got != "https://github.com/myfork/DKStore" {
		t.Fatalf("got %q", got)
	}

	// 官方镜像源暂未启用时自动回落至 GitHub 官方源
	fallbackCfg, err := NormalizeAndValidate(Config{Repo: "myfork/DKStore", Channel: ChannelMirror}, false)
	if err != nil || fallbackCfg.Channel != ChannelGitHub {
		t.Fatalf("expected fallback to github, got %+v, err=%v", fallbackCfg, err)
	}
	mCfg, err := NormalizeAndValidate(Config{Repo: "kukushouhou/DKStore", Channel: ChannelMirror}, true)
	if err != nil {
		t.Fatalf("mirror validate err: %v", err)
	}
	if !OfficialMirrorEnabled {
		if mCfg.Channel != ChannelGitHub || ActiveOfficialMirrorHost() != "" {
			t.Fatalf("expected disabled official mirror to fallback to github, got %+v", mCfg)
		}
	} else {
		wantMirror := "https://gh.h5.gg/https://github.com/kukushouhou/DKStore"
		if got := EffectiveBaseURL(mCfg); got != wantMirror {
			t.Fatalf("got %q, want %q", got, wantMirror)
		}
	}

	// 自定义前缀与换域名
	cPref, err := NormalizeAndValidate(Config{
		Repo:       "myfork/DKStore",
		Channel:    ChannelCustom,
		CustomHost: "gh-proxy.org/",
		CustomMode: CustomModePrefix,
	}, true)
	if err != nil {
		t.Fatalf("custom prefix err: %v", err)
	}
	if got := EffectiveBaseURL(cPref); got != "https://gh-proxy.org/https://github.com/myfork/DKStore" {
		t.Fatalf("got %q", got)
	}

	cHost, err := NormalizeAndValidate(Config{
		Repo:       "myfork/DKStore",
		Channel:    ChannelCustom,
		CustomHost: "https://kkgithub.com/",
		CustomMode: CustomModeHost,
	}, true)
	if err != nil {
		t.Fatalf("custom host err: %v", err)
	}
	if got := EffectiveBaseURL(cHost); got != "https://kkgithub.com/myfork/DKStore" {
		t.Fatalf("got %q", got)
	}
}

func TestParseSHA256SumsAndRejectHTML(t *testing.T) {
	if got := parseSHA256Sums("<html><body>404 Not Found</body></html>", "kukushouhou/DKStore", "linux", "amd64"); got != nil {
		t.Fatalf("expected nil for HTML body, got %+v", got)
	}

	hash := strings.Repeat("a1", 32)
	sums := fmt.Sprintf("%s  dkstore-linux-amd64-0.4.2.tar.gz\n%s  dkstore-windows-amd64-0.4.2.zip\n", hash, hash)
	got := parseSHA256Sums(sums, "kukushouhou/DKStore", "linux", "amd64")
	if got == nil {
		t.Fatal("expected parsed release")
	}
	if got.TagName != "v0.4.2" || got.AssetName != "dkstore-linux-amd64-0.4.2.tar.gz" || got.AssetSHA256 != hash {
		t.Fatalf("unexpected parsed result: %+v", got)
	}
}

func TestDownloadWithResumeAndSHA256(t *testing.T) {
	payload := bytes.Repeat([]byte("DKSTORE-BINARY-CHUNK-0123456789!"), 512) // 16 KB
	sum := sha256.Sum256(payload)
	expectedHex := hex.EncodeToString(sum[:])

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// 第一次故意只吐一半字节然后掐断连接，模拟跨境流中断
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:len(payload)/2])
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			return
		}
		rng := r.Header.Get("Range")
		if strings.HasPrefix(rng, "bytes=") {
			offStr := strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-")
			off, _ := strconv.Atoi(offStr)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-off))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[off:])
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	svc := New(nil, nil, t.TempDir(), nil)
	dest := filepath.Join(t.TempDir(), "pkg.bin")
	if err := svc.downloadWithResume(context.Background(), srv.URL+"/pkg.bin", dest, expectedHex, int64(len(payload))); err != nil {
		t.Fatalf("downloadWithResume failed: %v", err)
	}
	gotBytes, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(gotBytes, payload) {
		t.Fatalf("downloaded bytes mismatch (len=%d, want=%d)", len(gotBytes), len(payload))
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("expected at least 2 requests for resume, got %d", calls)
	}
}

func makeFakeELF(class byte, machine uint16) []byte {
	b := make([]byte, 64)
	copy(b[0:4], []byte{0x7f, 'E', 'L', 'F'})
	b[4] = class
	b[5] = 1 // little-endian
	binary.LittleEndian.PutUint16(b[18:20], machine)
	return b
}

func makeFakePE(machine uint16) []byte {
	b := make([]byte, 128)
	b[0] = 'M'
	b[1] = 'Z'
	binary.LittleEndian.PutUint32(b[0x3c:0x40], 0x40)
	copy(b[0x40:0x44], []byte{'P', 'E', 0, 0})
	binary.LittleEndian.PutUint16(b[0x44:0x46], machine)
	return b
}

func TestExtractAndVerifyArch(t *testing.T) {
	elfAmd64 := makeFakeELF(2, 62)
	if err := verifyBinaryArch(elfAmd64, "linux", "amd64"); err != nil {
		t.Fatalf("valid linux/amd64 rejected: %v", err)
	}
	if err := verifyBinaryArch(elfAmd64, "linux", "arm64"); err == nil {
		t.Fatal("expected arch mismatch when checking amd64 ELF against arm64")
	}

	peAmd64 := makeFakePE(0x8664)
	if err := verifyBinaryArch(peAmd64, "windows", "amd64"); err != nil {
		t.Fatalf("valid windows/amd64 rejected: %v", err)
	}

	// 测试模拟群晖 .spk（外层 tar -> package.tgz -> bin/dkstore）提取
	var innerBuf bytes.Buffer
	igz := gzip.NewWriter(&innerBuf)
	itw := tar.NewWriter(igz)
	_ = itw.WriteHeader(&tar.Header{Name: "bin/dkstore", Mode: 0o755, Size: int64(len(elfAmd64)), Typeflag: tar.TypeReg})
	_, _ = itw.Write(elfAmd64)
	_ = itw.Close()
	_ = igz.Close()

	var spkBuf bytes.Buffer
	stw := tar.NewWriter(&spkBuf)
	_ = stw.WriteHeader(&tar.Header{Name: "package.tgz", Mode: 0o644, Size: int64(innerBuf.Len()), Typeflag: tar.TypeReg})
	_, _ = stw.Write(innerBuf.Bytes())
	_ = stw.Close()

	extracted, err := extractBinaryFromBytes(spkBuf.Bytes(), "dkstore.spk", 0)
	if err != nil || !bytes.Equal(extracted, elfAmd64) {
		t.Fatalf("extract from .spk failed: err=%v", err)
	}

	// 测试 Windows .zip 提取
	var zBuf bytes.Buffer
	zw := zip.NewWriter(&zBuf)
	zf, _ := zw.Create("dkstore.exe")
	_, _ = zf.Write(peAmd64)
	_ = zw.Close()

	extractedPE, err := extractBinaryFromBytes(zBuf.Bytes(), "dkstore.zip", 0)
	if err != nil || !bytes.Equal(extractedPE, peAmd64) {
		t.Fatalf("extract from .zip failed: err=%v", err)
	}
}

func TestPolicyNotifyOnceAndManualSilent(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	oldVer := version.Version
	version.Version = "v0.4.0"
	defer func() { version.Version = oldVer }()

	var pushCount int32
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	relSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v0.5.0","notesZh":"test"}`))
	}))
	defer relSrv.Close()

	ntfSvc := notify.New(st)
	if err := ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:             "zh-CN",
		CheckIntervalHours: 6,
		Channel:            "webhook",
		WebhookURL:         webhookSrv.URL,
	}); err != nil {
		t.Fatalf("SaveConfig notify: %v", err)
	}

	svc := New(nil, st, dataDir, nil)
	svc.SetAutoUpdateDeps(ntfSvc, func() bool { return false })

	// 1. 手动更新（manual）：检测新版本但不发外部推送
	if _, err := svc.SaveConfig(ctx, Config{
		Policy:     PolicyManual,
		Repo:       "kukushouhou/DKStore",
		Channel:    ChannelCustom,
		CustomHost: relSrv.URL,
		CustomMode: CustomModeHost,
	}); err != nil {
		t.Fatalf("SaveConfig manual: %v", err)
	}
	svc.runScheduledPolicyCheck(ctx)
	if got := atomic.LoadInt32(&pushCount); got != 0 {
		t.Fatalf("manual policy should not push notification, got %d", got)
	}

	// 2. 仅推送提醒（notify）：同一新版本仅推送一次
	if _, err := svc.SaveConfig(ctx, Config{
		Policy:     PolicyNotify,
		Repo:       "kukushouhou/DKStore",
		Channel:    ChannelCustom,
		CustomHost: relSrv.URL,
		CustomMode: CustomModeHost,
	}); err != nil {
		t.Fatalf("SaveConfig notify: %v", err)
	}
	svc.runScheduledPolicyCheck(ctx)
	svc.runScheduledPolicyCheck(ctx)
	if got := atomic.LoadInt32(&pushCount); got != 1 {
		t.Fatalf("notify policy should push exactly once per version, got %d", got)
	}
}
