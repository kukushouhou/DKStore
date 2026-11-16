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
	"time"

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

func TestPolicyNotify_UnconfiguredFailureQuietHoursAndChannelSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	oldVer := version.Version
	version.Version = "v0.5.0"
	defer func() { version.Version = oldVer }()

	var relVer atomic.Value
	relVer.Store("v0.6.0")
	relSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"version":%q,"notesZh":"测试更新"}`, relVer.Load().(string))
	}))
	defer relSrv.Close()

	var failHits, hitsA, hitsB int32
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&failHits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsA, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hitsB, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	ntfSvc := notify.New(st)
	svc := New(nil, st, dataDir, nil)
	svc.SetAutoUpdateDeps(ntfSvc, func() bool { return false })

	readNotifiedVer := func() string {
		var v string
		_ = st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, settingKeyNotifiedVer).Scan(&v)
		return v
	}

	// 1. 向导选择「仅推送提醒」但尚未配置通知渠道（含免打扰时段）：不得将 v0.6.0 误记为已提醒
	_ = ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:             "zh-CN",
		CheckIntervalHours: 6,
		Channel:            "",
		QuietHoursStart:    "00:00",
		QuietHoursEnd:      "23:59",
	})
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
	if got := readNotifiedVer(); got != "" {
		t.Fatalf("unconfigured channel must not record notified version, got %q", got)
	}

	// 2. 设置页先从 manual 切到 notify（此时尚未填好下方渠道）：同样不得误消耗提醒机会
	_, _ = svc.SaveConfig(ctx, Config{
		Policy:     PolicyManual,
		Repo:       "kukushouhou/DKStore",
		Channel:    ChannelCustom,
		CustomHost: relSrv.URL,
		CustomMode: CustomModeHost,
	})
	svc.runScheduledPolicyCheck(ctx)
	_, _ = svc.SaveConfig(ctx, Config{
		Policy:     PolicyNotify,
		Repo:       "kukushouhou/DKStore",
		Channel:    ChannelCustom,
		CustomHost: relSrv.URL,
		CustomMode: CustomModeHost,
	})
	svc.runScheduledPolicyCheck(ctx)
	if got := readNotifiedVer(); got != "" {
		t.Fatalf("switching manual->notify before setting channel must not consume quota, got %q", got)
	}

	// 3. 通知发送失败（HTTP 500）：不得记录已提醒
	_ = ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:             "zh-CN",
		CheckIntervalHours: 6,
		Channel:            "webhook",
		WebhookURL:         failSrv.URL,
	})
	svc.runScheduledPolicyCheck(ctx)
	if atomic.LoadInt32(&failHits) != 1 || readNotifiedVer() != "" {
		t.Fatalf("HTTP 500 failure must not record notified version, failHits=%d notified=%q", failHits, readNotifiedVer())
	}

	// 4. 已配有效渠道但处于免打扰时段：返回 30m 重试间隔且不消耗提醒机会
	_ = ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:             "zh-CN",
		CheckIntervalHours: 6,
		Channel:            "webhook",
		WebhookURL:         srvA.URL,
		QuietHoursStart:    "00:00",
		QuietHoursEnd:      "23:59",
	})
	if wait := svc.runScheduledPolicyCheck(ctx); wait != 30*time.Minute {
		t.Fatalf("expected 30m retry during quiet hours, got %v", wait)
	}
	if atomic.LoadInt32(&hitsA) != 0 || readNotifiedVer() != "" {
		t.Fatalf("quiet hours must not send or record notified version, hitsA=%d notified=%q", hitsA, readNotifiedVer())
	}

	// 5. 启动真实后台调度协程，用户解除免打扰并保存有效渠道后调用 Wakeup：立即补推一次且不重复
	svc.bootDelay = 5 * time.Millisecond
	svc.firstCheckDelay = 10 * time.Second
	svc.StartScheduler(ctx)
	time.Sleep(25 * time.Millisecond)

	_ = ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:             "zh-CN",
		CheckIntervalHours: 6,
		Channel:            "webhook",
		WebhookURL:         srvA.URL,
	})
	svc.Wakeup(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&hitsA) == 0 {
		time.Sleep(15 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hitsA); got != 1 {
		t.Fatalf("expected immediate delivery on Wakeup after configuring channel, got hitsA=%d", got)
	}
	if got := readNotifiedVer(); got != "v0.6.0" {
		t.Fatalf("expected notified_ver=v0.6.0 after delivery, got %q", got)
	}

	// 6. 切换通知渠道（Webhook A -> Bark B）：已成功送达的 v0.6.0 不重复推送，新版本 v0.7.0 走新渠道
	_ = ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:             "zh-CN",
		CheckIntervalHours: 6,
		Channel:            "bark",
		WebhookURL:         srvB.URL,
	})
	svc.runScheduledPolicyCheck(ctx)
	if got := atomic.LoadInt32(&hitsB); got != 0 {
		t.Fatalf("switching channel must not re-push already delivered v0.6.0, got hitsB=%d", got)
	}

	relVer.Store("v0.7.0")
	svc.runScheduledPolicyCheck(ctx)
	if got := atomic.LoadInt32(&hitsB); got != 1 || readNotifiedVer() != "v0.7.0" {
		t.Fatalf("expected new release v0.7.0 to push once on new channel, got hitsB=%d notified=%q", hitsB, readNotifiedVer())
	}
}

func TestFlushPendingUpgradedNotice_RetainsOnTransientFailureAndClearsWhenNoChannel(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	oldVer := version.Version
	version.Version = "v0.5.0"
	defer func() { version.Version = oldVer }()

	ntfSvc := notify.New(st)
	svc := New(nil, st, dataDir, nil)
	svc.SetAutoUpdateDeps(ntfSvc, nil)

	hasPending := func() bool {
		var raw string
		err := st.DB.QueryRowContext(ctx, `SELECT v FROM setting WHERE k=?`, settingKeyUpgradedNotify).Scan(&raw)
		return err == nil && raw != ""
	}

	// 1. 未配置通知渠道时：直接清理升级标记，不留陈旧待发记录
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO setting(k,v) VALUES (?, ?)`, settingKeyUpgradedNotify, `{"from":"v0.4.0","to":"v0.5.0"}`)
	svc.flushPendingUpgradedNotice(ctx)
	if hasPending() {
		t.Fatal("expected pending upgraded notice to clear when no channel is configured")
	}

	// 2. 已配渠道但启动瞬间遇到 HTTP 500：保留记录，恢复 200 后成功补推并清理
	var shouldFail atomic.Bool
	shouldFail.Store(true)
	var hits int32
	whSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer whSrv.Close()

	_ = ntfSvc.SaveConfig(ctx, notify.Config{
		Locale:     "zh-CN",
		Channel:    "webhook",
		WebhookURL: whSrv.URL,
	})
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO setting(k,v) VALUES (?, ?)`, settingKeyUpgradedNotify, `{"from":"v0.4.0","to":"v0.5.0"}`)

	svc.flushPendingUpgradedNotice(ctx)
	if !hasPending() || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("expected pending upgraded notice to remain after HTTP 502, hasPending=%v hits=%d", hasPending(), hits)
	}

	shouldFail.Store(false)
	svc.flushPendingUpgradedNotice(ctx)
	if hasPending() || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected pending upgraded notice to deliver once and clear after recovery, hasPending=%v hits=%d", hasPending(), hits)
	}
}
