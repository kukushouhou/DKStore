package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/transport"
)

type fakeConn struct{ key string }

func TestDialSSHRefreshingHostKey(t *testing.T) {
	var seen []string
	dial := func(_ context.Context, cfg transport.SSHConfig) (*fakeConn, error) {
		seen = append(seen, cfg.TrustedHostKey)
		if cfg.TrustedHostKey != "" && cfg.TrustedHostKey != "SHA256:new" {
			return nil, &transport.HostKeyMismatchError{Expected: cfg.TrustedHostKey, Presented: "SHA256:new"}
		}
		return &fakeConn{key: "SHA256:new"}, nil
	}

	// 指纹变化：自动用新指纹重连，并返回旧指纹
	c, prev, err := dialSSHRefreshingHostKey(context.Background(), transport.SSHConfig{TrustedHostKey: "SHA256:old"}, dial)
	if err != nil || c == nil || prev != "SHA256:old" {
		t.Fatalf("expected refreshed connection with prev=old, got c=%v prev=%q err=%v", c, prev, err)
	}
	if len(seen) != 2 || seen[1] != "SHA256:new" {
		t.Fatalf("expected redial with new fingerprint, seen=%v", seen)
	}

	// 指纹一致：不标记变化
	seen = nil
	_, prev, err = dialSSHRefreshingHostKey(context.Background(), transport.SSHConfig{TrustedHostKey: "SHA256:new"}, dial)
	if err != nil || prev != "" || len(seen) != 1 {
		t.Fatalf("expected single dial without change, prev=%q err=%v seen=%v", prev, err, seen)
	}

	// 首次连接（TOFU）
	_, prev, err = dialSSHRefreshingHostKey(context.Background(), transport.SSHConfig{AllowTOFU: true}, dial)
	if err != nil || prev != "" {
		t.Fatalf("expected TOFU success, prev=%q err=%v", prev, err)
	}

	// 其他错误原样返回
	other := errors.New("auth failed")
	_, _, err = dialSSHRefreshingHostKey(context.Background(), transport.SSHConfig{}, func(context.Context, transport.SSHConfig) (*fakeConn, error) {
		return nil, other
	})
	if !errors.Is(err, other) {
		t.Fatalf("expected passthrough error, got %v", err)
	}
}

// fakeDSM 模拟开启 2FA 的群晖：OTP 123456 换取设备令牌 DID1，之后凭 device_id 登录。
type fakeDSM struct {
	mu        sync.Mutex
	otpLogins int
	didLogins int
}

func (f *fakeDSM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	api, method := r.PostForm.Get("api"), r.PostForm.Get("method")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch api + "." + method {
	case "SYNO.API.Auth.login":
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.PostForm.Get("otp_code") == "123456" {
			f.otpLogins++
			write(map[string]any{"success": true, "data": map[string]any{"sid": "s1", "did": "DID1"}})
			return
		}
		if r.PostForm.Get("device_id") == "DID1" {
			f.didLogins++
			write(map[string]any{"success": true, "data": map[string]any{"sid": "s2"}})
			return
		}
		write(map[string]any{"success": false, "error": map[string]any{"code": 403}})
	case "SYNO.DSM.Info.getinfo":
		write(map[string]any{"success": true, "data": map[string]any{"model": "DS920+", "version_string": "DSM 7.2"}})
	case "SYNO.Core.System.info":
		write(map[string]any{"success": true, "data": map[string]any{"cpu_vendor": "INTEL"}})
	default:
		write(map[string]any{"success": true, "data": map[string]any{}})
	}
}

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secret.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewRegistry(st, box)
}

func countSecrets(t *testing.T, r *Registry) int {
	t.Helper()
	var n int
	if err := r.st.DB.QueryRow(`SELECT COUNT(*) FROM secret`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDSMHostLifecycleWithDeviceTokenNonce(t *testing.T) {
	ctx := context.Background()
	f := &fakeDSM{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := newTestRegistry(t)

	in := CreateOrUpdateInput{
		Name: "nas", Kind: "docker", Transport: "dsm_api",
		DSMURL: srv.URL, DSMUser: "admin", DSMSecret: "p1", DSMOTPCode: "123456",
	}
	res, err := r.TestConnection(ctx, in, i18n.EnUS)
	if err != nil || !res.OK || res.DeviceTokenNonce == "" {
		t.Fatalf("test connection failed: res=%+v err=%v", res, err)
	}
	if res.Info == nil || res.Info.Arch != "amd64" {
		t.Fatalf("expected arch amd64 from cpu_vendor, got %+v", res.Info)
	}

	// 保存时带回 nonce：不得再次使用 OTP
	in.DeviceTokenNonce = res.DeviceTokenNonce
	h, err := r.Create(ctx, in, i18n.EnUS)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if h.Kind != "synology" {
		t.Fatalf("dsm_api must force kind=synology, got %s", h.Kind)
	}
	if f.otpLogins != 1 {
		t.Fatalf("OTP must be used only once, got %d", f.otpLogins)
	}
	if n := countSecrets(t, r); n != 2 {
		t.Fatalf("expected 2 secrets (password + device token), got %d", n)
	}
	refsBefore, _ := loadRefs(ctx, r.st.DB, h.ID)

	// nonce 一次性
	if tok := r.takeDeviceToken(res.DeviceTokenNonce, srv.URL, "admin"); tok != "" {
		t.Fatal("nonce must be one-time")
	}

	// 编辑时测试连接复用已保存凭据（不填密码）
	res2, err := r.TestConnection(ctx, CreateOrUpdateInput{
		HostID: h.ID, Name: "nas", Transport: "dsm_api", DSMURL: srv.URL, DSMUser: "admin",
	}, i18n.ZhCN)
	if err != nil || !res2.OK {
		t.Fatalf("test with saved credentials failed: res=%+v err=%v", res2, err)
	}
	if res2.DeviceTokenNonce != "" {
		t.Fatal("unchanged device token must not be stashed again")
	}

	// 更新密码：旧密码 secret 删除，设备令牌未变不重复写入
	h2, err := r.Update(ctx, h.ID, CreateOrUpdateInput{
		Name: "nas2", Kind: "docker", Transport: "dsm_api", DSMURL: srv.URL, DSMUser: "admin", DSMSecret: "p2",
	}, i18n.EnUS)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if h2.Name != "nas2" || h2.Kind != "synology" {
		t.Fatalf("unexpected host after update: %+v", h2)
	}
	refsAfter, _ := loadRefs(ctx, r.st.DB, h.ID)
	if refsAfter.dsm == refsBefore.dsm || refsAfter.did != refsBefore.did {
		t.Fatalf("expected new password ref and unchanged token ref: before=%+v after=%+v", refsBefore, refsAfter)
	}
	if n := countSecrets(t, r); n != 2 {
		t.Fatalf("old password secret must be deleted, got %d secrets", n)
	}

	// 删除主机同时删除凭据
	if err := r.Delete(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if n := countSecrets(t, r); n != 0 {
		t.Fatalf("secrets must be deleted with host, got %d", n)
	}
}

func TestUpdateValidation(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	_, err := r.Create(ctx, CreateOrUpdateInput{Name: "x", Transport: "ssh"}, i18n.EnUS)
	if err == nil || i18n.LocalizeErr(i18n.EnUS, err) != i18n.HostText(i18n.EnUS, "host.ssh_required") {
		t.Fatalf("expected ssh_required, got %v", err)
	}
	_, err = r.Create(ctx, CreateOrUpdateInput{Name: "x", Transport: "dsm_api", DSMURL: "http://127.0.0.1:1"}, i18n.ZhCN)
	if err == nil || i18n.LocalizeErr(i18n.ZhCN, err) != i18n.HostText(i18n.ZhCN, "host.dsm_required") {
		t.Fatalf("expected dsm_required, got %v", err)
	}
	_, err = r.Create(ctx, CreateOrUpdateInput{Name: "x", Transport: "ftp"}, i18n.EnUS)
	if err == nil {
		t.Fatal("expected invalid transport error")
	}
}

func TestDeviceTokenStashBinding(t *testing.T) {
	r := newTestRegistry(t)
	n := r.stashDeviceToken("tok", "http://nas:5000", "admin")
	if got := r.takeDeviceToken(n, "http://other:5000", "admin"); got != "" {
		t.Fatal("token must be bound to DSM url")
	}
	n = r.stashDeviceToken("tok", "http://nas:5000", "admin")
	if got := r.takeDeviceToken(n, "http://nas:5000", "admin"); got != "tok" {
		t.Fatalf("expected tok, got %q", got)
	}
}

func TestListRuntimeContainers_ReusesAdapterSessionAcrossPolls(t *testing.T) {
	ctx := context.Background()
	f := &fakeDSM{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	r := newTestRegistry(t)
	defer r.Close()

	in := CreateOrUpdateInput{
		Name: "nas-a", Kind: "synology", Transport: "dsm_api",
		DSMURL: srv.URL, DSMUser: "admin", DSMSecret: "p1", DSMOTPCode: "123456",
	}
	h, err := r.Create(ctx, in, i18n.ZhCN)
	if err != nil {
		t.Fatalf("create host failed: %v", err)
	}

	f.mu.Lock()
	didBefore := f.didLogins
	f.mu.Unlock()

	// Consecutive fast + stats + poll calls must reuse the pooled DSM session (only 1 login)
	if _, err := r.ListRuntimeContainers(ctx, h.ID, false); err != nil {
		t.Fatalf("fast ListRuntimeContainers failed: %v", err)
	}
	if _, err := r.ListRuntimeContainers(ctx, h.ID, true); err != nil {
		t.Fatalf("stats ListRuntimeContainers failed: %v", err)
	}
	if _, err := r.ListRuntimeContainers(ctx, "", true); err != nil {
		t.Fatalf("poll ListRuntimeContainers failed: %v", err)
	}

	f.mu.Lock()
	didAfter := f.didLogins
	f.mu.Unlock()
	if didAfter-didBefore != 1 {
		t.Fatalf("expected pooled runtime adapter to login once across 3 calls, got %d logins", didAfter-didBefore)
	}
}

func TestListCachedRuntimeContainers_InventoryFallbackAndSnapshotPersistence(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	defer r.Close()

	now := time.Now().Unix()
	_, err := r.st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, address, port, ssh_user, status, created_at) VALUES ('h-ssh', '客厅软路由', 'docker', 'ssh', '192.0.2.99', 22, 'root', 'online', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = r.st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h-ssh/compose/homebox', 'h-ssh', 'homebox', 'compose', ?)`, now)
	_, _ = r.st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, image_ref, state, updated_at) VALUES ('h-ssh/compose/homebox/web', 'h-ssh/compose/homebox', 'web', 'ghcr.io/sysadminsmedia/homebox:latest', 'running', ?)`, now)
	_, _ = r.st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, image_ref, state, updated_at) VALUES ('h-ssh/compose/homebox/db', 'h-ssh/compose/homebox', 'db', 'postgres:16-alpine', 'running', ?)`, now)

	// 1. Before any runtime snapshot exists, ListCachedRuntimeContainers falls back to SQLite app/service instantly (0 network IO even to unreachable 192.0.2.99)
	t0 := time.Now()
	fallbackItems, err := r.ListCachedRuntimeContainers(ctx, "")
	if err != nil {
		t.Fatalf("ListCachedRuntimeContainers fallback failed: %v", err)
	}
	if time.Since(t0) > 200*time.Millisecond {
		t.Fatalf("ListCachedRuntimeContainers must not do network IO, took %v", time.Since(t0))
	}
	if len(fallbackItems) != 1 || fallbackItems[0].HostName != "客厅软路由" || len(fallbackItems[0].Containers) != 2 || fallbackItems[0].StatsReady {
		t.Fatalf("unexpected inventory fallback items: %+v", fallbackItems)
	}

	// 2. Save a fresh runtime snapshot with stats and verify instant retrieval
	r.saveHostRuntimeSnapshot(ctx, "h-ssh", true, []platform.AppRuntimeTree{
		{
			Key:          "h-ssh/compose/homebox",
			Name:         "homebox",
			Source:       platform.SourceCompose,
			RunningCount: 2,
			TotalCount:   2,
			CPUPercent:   12.5,
			MemUsage:     256 * 1024 * 1024,
			StatsReady:   true,
			Containers: []platform.ContainerRuntimeStat{
				{ID: "c1", Name: "homebox-web-1", Service: "web", Image: "ghcr.io/sysadminsmedia/homebox:latest", State: "running", Uptime: "Up 10 minutes", StartedAt: now - 600, CPUPercent: 8.5, MemUsage: 128 * 1024 * 1024},
				{ID: "c2", Name: "homebox-db-1", Service: "db", Image: "postgres:16-alpine", State: "running", Uptime: "Up 10 minutes", StartedAt: now - 600, CPUPercent: 4.0, MemUsage: 128 * 1024 * 1024},
			},
		},
	})

	freshSnap, err := r.ListCachedRuntimeContainers(ctx, "h-ssh")
	if err != nil || len(freshSnap) != 1 || !freshSnap[0].StatsReady || freshSnap[0].CPUPercent != 12.5 {
		t.Fatalf("expected fresh snapshot with statsReady=true, got %+v err=%v", freshSnap, err)
	}

	// 3. Age out snapshot (> 60s): container tree still renders instantly, while stale CPU/Mem and literal Uptime string are cleared
	_, _ = r.st.DB.ExecContext(ctx, `UPDATE host_runtime_snapshot SET updated_at=? WHERE host_id='h-ssh'`, now-120)
	agedSnap, err := r.ListCachedRuntimeContainers(ctx, "h-ssh")
	if err != nil || len(agedSnap) != 1 || agedSnap[0].StatsReady || agedSnap[0].Containers[0].Uptime != "" {
		t.Fatalf("expected aged snapshot to mark StatsReady=false and clear stale literal Uptime, got %+v err=%v", agedSnap, err)
	}
}
