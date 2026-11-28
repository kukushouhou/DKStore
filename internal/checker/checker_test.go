package checker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kukushouhou/DKStore/internal/classify"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
)

func newTestChecker(t *testing.T, baseURLOverride map[string]string) (*Service, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secret.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("master key: %v", err)
	}
	hm := host.NewRegistry(st, box)
	inv := inventory.New(st, hm)
	reg := registry.New(st, box)
	reg.BaseURLOverride = baseURLOverride
	ign := ignore.New(st)
	ntf := notify.New(st)
	return New(slog.Default(), st, hm, inv, reg, ign, ntf), st
}

func TestTrigger_MergesPendingRunAndExecutesAfterFinish(t *testing.T) {
	chk, _ := newTestChecker(t, nil)

	type call struct {
		trigger     string
		hostID      string
		bypassCache bool
	}
	var (
		mu      sync.Mutex
		calls   []call
		unblock = make(chan struct{})
		doneAll = make(chan struct{})
	)
	chk.runFn = func(ctx context.Context, trigger, hostID string, bypassCache bool) {
		mu.Lock()
		idx := len(calls)
		calls = append(calls, call{trigger: trigger, hostID: hostID, bypassCache: bypassCache})
		mu.Unlock()
		if idx == 0 {
			<-unblock
		}
		chk.finishOrRunPending()
		if idx == 1 {
			close(doneAll)
		}
	}

	// Start first run
	chk.Trigger("manual", "host-a", false)
	// Trigger two more runs while first is running: host-a + host-b should merge to "" (all hosts) and bypassCache=true
	chk.Trigger("schedule", "host-a", false)
	chk.Trigger("manual", "host-b", true)

	close(unblock)
	select {
	case <-doneAll:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for pending run to execute")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(calls))
	}
	if calls[1].hostID != "" || !calls[1].bypassCache {
		t.Fatalf("expected merged pending run to have hostID='' and bypassCache=true, got %+v", calls[1])
	}
}

func TestCheckSingleService_NoRepoDigest_Distinguishes404VsRateLimit(t *testing.T) {
	statusToReturn := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusToReturn)
	}))
	defer srv.Close()

	chk, _ := newTestChecker(t, map[string]string{
		"ghcr.io": srv.URL,
	})
	ctx := context.Background()
	svc := svcRow{
		Key:           "h1:dsm_project:app:web",
		AppKey:        "h1:dsm_project:app",
		AppName:       "app",
		AppSource:     "dsm_project",
		HostID:        "h1",
		HostName:      "NAS",
		HostTransport: "dsm_api",
		Name:          "web",
		ImageRef:      "ghcr.io/org/local-only:1.0.0",
		ImageID:       "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		RepoDigest:    "",
		Platform:      "linux/amd64",
	}

	// 1. 404 Not Found with empty RepoDigest -> KindUncomparable
	out, _, _, _ := chk.checkSingleService(ctx, svc, true)
	if out.Kind != classify.KindUncomparable {
		t.Fatalf("expected KindUncomparable on 404 without RepoDigest, got %s", out.Kind)
	}

	// 2. 429 Rate Limited with empty RepoDigest -> KindCheckFailed (not swallowed as uncomparable)
	statusToReturn = http.StatusTooManyRequests
	out2, errKind, _, _ := chk.checkSingleService(ctx, svc, true)
	if out2.Kind != classify.KindCheckFailed {
		t.Fatalf("expected KindCheckFailed on 429 without RepoDigest, got %s", out2.Kind)
	}
	if errKind != string(registry.ErrRateLimited) {
		t.Fatalf("expected errKind=rate_limited, got %s", errKind)
	}
}

func TestSaveFinding_PreservesNotifiedAtAcrossCheckFailedAndGetApp(t *testing.T) {
	chk, st := newTestChecker(t, nil)
	ctx := context.Background()
	now := time.Now().Unix()

	_, err := st.DB.ExecContext(ctx, `
		INSERT INTO host(id, name, kind, transport, status, created_at)
		VALUES ('h1', 'NAS', 'docker', 'local', 'online', ?)`, now)
	if err != nil {
		t.Fatalf("insert host: %v", err)
	}
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO app(key, host_id, name, source, updated_at)
		VALUES ('h1:compose:stack', 'h1', 'stack', 'compose', ?)`, now)
	if err != nil {
		t.Fatalf("insert app: %v", err)
	}
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, platform, updated_at)
		VALUES
		('h1:compose:stack:web', 'h1:compose:stack', 'web', 'running', 'nginx:1.26.0', 'sha256:aaa', 'sha256:aaa', 'linux/amd64', ?),
		('h1:compose:stack:db', 'h1:compose:stack', 'db', 'running', 'postgres:17.1', 'sha256:bbb', 'sha256:bbb', 'linux/amd64', ?)`,
		now, now)
	if err != nil {
		t.Fatalf("insert services: %v", err)
	}

	svc := svcRow{
		Key:      "h1:compose:stack:web",
		AppKey:   "h1:compose:stack",
		AppName:  "stack",
		HostID:   "h1",
		HostName: "NAS",
		Name:     "web",
		ImageRef: "nginx:1.26.0",
	}

	updOut := classify.Output{
		Kind:           classify.KindVersionUpdate,
		Category:       classify.CategoryNormal,
		CurrentVersion: "1.26.0",
		TargetVersion:  "1.27.0",
		TargetRef:      "nginx:1.27.0",
		TargetDigest:   "sha256:ccc",
	}

	// 1. First detection -> isNew = true, then mark notified_at
	if !chk.saveFinding(ctx, 1, svc, updOut, "", "", nil) {
		t.Fatal("expected first update detection to return isNewUpdate=true")
	}
	_, _ = st.DB.ExecContext(ctx, `UPDATE finding SET notified_at=? WHERE service_key=?`, now, svc.Key)

	// 2. Transient network failure -> should NOT write 'resolved' history and should preserve notified_at
	failOut := classify.Output{Kind: classify.KindCheckFailed}
	if chk.saveFinding(ctx, 2, svc, failOut, "network", "timeout", nil) {
		t.Fatal("expected check_failed to return isNewUpdate=false")
	}
	var resolvedCount int
	_ = st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM finding_history WHERE service_key=? AND change='resolved'`, svc.Key).Scan(&resolvedCount)
	if resolvedCount != 0 {
		t.Fatalf("expected 0 resolved history rows on check_failed, got %d", resolvedCount)
	}

	// 3. Recovery with same target version -> should NOT trigger duplicate notification
	if chk.saveFinding(ctx, 3, svc, updOut, "", "", nil) {
		t.Fatal("expected recovery from check_failed with existing notified_at to return isNewUpdate=false")
	}

	// 4. GetApp returns all services even when one is normal and one is up_to_date
	card, err := chk.GetApp(ctx, "h1:compose:stack", i18n.ZhCN)
	if err != nil || card == nil {
		t.Fatalf("GetApp: err=%v card=%v", err, card)
	}
	if len(card.Services) != 2 {
		t.Fatalf("expected GetApp to return all 2 services, got %d", len(card.Services))
	}
}

func TestOverview_ThreeGroupsNoSlashConcatenationAndIgnoreBadges(t *testing.T) {
	chk, st := newTestChecker(t, nil)
	ctx := context.Background()
	now := time.Now().Unix()

	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', '本机群晖 NAS', 'synology', 'dsm_api', 'online', ?)`, now)
	_, _ = st.DB.ExecContext(ctx, `
		INSERT INTO app(key, host_id, name, source, project_json, updated_at) VALUES
		('h1/dsm_project/teslamate-cn', 'h1', 'teslamate-cn', 'dsm_project', '{"name":"teslamate-cn","dsmProjectId":"uuid-1","sourceReadable":true}', ?),
		('h1/container/chromium', 'h1', 'chromium', 'standalone', '', ?)`, now, now)
	_, _ = st.DB.ExecContext(ctx, `
		INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, platform, labels_json, updated_at) VALUES
		('h1/dsm_project/teslamate-cn/teslamate', 'h1/dsm_project/teslamate-cn', 'teslamate', 'running', 'teslamate/teslamate:1.30.0', 'sha256:11', 'sha256:11', 'linux/amd64', '', ?),
		('h1/dsm_project/teslamate-cn/mosquitto', 'h1/dsm_project/teslamate-cn', 'mosquitto', 'running', 'eclipse-mosquitto:2', 'sha256:22', 'sha256:22', 'linux/amd64', '', ?),
		('h1/container/chromium/main', 'h1/container/chromium', 'chromium', 'running', 'linuxserver/chromium:131.0.6778.85', 'sha256:33', 'sha256:33', 'linux/amd64', '', ?)`,
		now, now, now)
	_, _ = st.DB.ExecContext(ctx, `
		INSERT INTO finding(service_key, kind, category, reason, current_version, target_version, target_ref, target_digest, checked_at) VALUES
		('h1/dsm_project/teslamate-cn/teslamate', 'version_update', 'normal', '', '1.30.0', '1.31.0', 'teslamate/teslamate:1.31.0', 'sha256:11b', ?),
		('h1/dsm_project/teslamate-cn/mosquitto', 'digest_update', 'caution', 'database|Eclipse Mosquitto|mq', '2.0.18', '2.0.20', 'eclipse-mosquitto:2', 'sha256:22b', ?),
		('h1/container/chromium/main', 'version_update', 'caution', 'major_bump', '131.0.6778.85', '132.0.6834.57', 'linuxserver/chromium:132.0.6834.57', 'sha256:33b', ?)`,
		now, now, now)

	// 为 chromium 设置锁定前缀 131 -> 132.0.6834.57 应被抑制并归入 noUpdate，且带 Ignored=true & IgnorePrefix="131"
	if err := chk.ign.SetWithOptions(ctx, ignore.SetOptions{
		ServiceKeys: []string{"h1/container/chromium/main"},
		Mode:        ignore.ModePrefix,
		Prefix:      "131",
	}); err != nil {
		t.Fatal(err)
	}

	ov, err := chk.Overview(ctx, "", i18n.ZhCN)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(ov.Normal) != 1 || ov.Normal[0].Name != "teslamate" || ov.Normal[0].ProjectName != "teslamate-cn" || ov.Normal[0].TotalServiceCount != 2 || len(ov.Normal[0].AllServices) != 2 || !ov.Normal[0].ComposeEditable {
		t.Fatalf("unexpected normal card: %+v", ov.Normal)
	}
	if len(ov.Caution) != 1 || ov.Caution[0].Name != "mosquitto" || ov.Caution[0].ProjectName != "teslamate-cn" || ov.Caution[0].RoleKind != "mq" {
		t.Fatalf("unexpected caution card (must be 'mosquitto', not slash concat): %+v", ov.Caution)
	}
	if len(ov.NoUpdate) != 1 || ov.NoUpdate[0].Group != "no_update" || !ov.NoUpdate[0].Ignored || ov.NoUpdate[0].IgnorePrefix != "131" || ov.NoUpdate[0].SummaryCurrent != "131.0.6778.85" {
		t.Fatalf("unexpected noUpdate card: %+v", ov.NoUpdate)
	}
}

func TestRunCheck_ConcurrentAcrossRegistriesAndPerRegistryLimit(t *testing.T) {
	var (
		inFlightDocker atomic.Int32
		maxDocker      atomic.Int32
		inFlightTotal  atomic.Int32
		maxTotal       atomic.Int32
	)
	recordEnter := func(isDocker bool) func() {
		curT := inFlightTotal.Add(1)
		for {
			prev := maxTotal.Load()
			if curT <= prev || maxTotal.CompareAndSwap(prev, curT) {
				break
			}
		}
		if isDocker {
			curD := inFlightDocker.Add(1)
			for {
				prev := maxDocker.Load()
				if curD <= prev || maxDocker.CompareAndSwap(prev, curD) {
					break
				}
			}
		}
		return func() {
			if isDocker {
				inFlightDocker.Add(-1)
			}
			inFlightTotal.Add(-1)
		}
	}

	dockerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done := recordEnter(true)
		defer done()
		time.Sleep(45 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/tags/1.0.0") {
			_, _ = w.Write([]byte(`{"digest":"sha256:newdig","images":[{"os":"linux","architecture":"amd64","digest":"sha256:newdig"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"next":"","results":[{"name":"1.0.0","digest":"sha256:olddig","images":[]},{"name":"1.1.0","digest":"sha256:newdig","images":[]}]}`))
	}))
	defer dockerSrv.Close()

	makeOCISrv := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			done := recordEnter(false)
			defer done()
			time.Sleep(45 * time.Millisecond)
			if strings.HasSuffix(r.URL.Path, "/tags/list") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"tags":["1.0.0","1.2.0"]}`))
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:oci100")
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfg100"}}`))
			}
		}))
	}
	ghcrSrv := makeOCISrv()
	defer ghcrSrv.Close()
	lscrSrv := makeOCISrv()
	defer lscrSrv.Close()

	chk, st := newTestChecker(t, map[string]string{
		"docker.io":      dockerSrv.URL,
		"hub.docker.com": dockerSrv.URL,
		"ghcr.io":        ghcrSrv.URL,
		"lscr.io":        lscrSrv.URL,
	})
	ctx := context.Background()
	now := time.Now().Unix()

	// transport='ssh' without credentials fails fast in SyncHost without touching seeded services
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', 'NAS', 'docker', 'ssh', 'online', ?)`, now)

	// 6 docker.io services + 3 ghcr.io services + 3 lscr.io services = 12 services across 3 registries
	for i := 0; i < 12; i++ {
		appKey := fmt.Sprintf("h1/container/app-%02d", i)
		svcKey := appKey + "/main"
		appName := fmt.Sprintf("app-%02d", i)
		var img, dig string
		switch {
		case i < 6:
			img = fmt.Sprintf("org/dk-%d:1.0.0", i)
			dig = "sha256:newdig"
		case i < 9:
			img = fmt.Sprintf("ghcr.io/org/gh-%d:1.0.0", i)
			dig = "sha256:oci100"
		default:
			img = fmt.Sprintf("lscr.io/org/ls-%d:1.0.0", i)
			dig = "sha256:oci100"
		}
		_, err := st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES (?, 'h1', ?, 'standalone', ?)`, appKey, appName, now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, platform, updated_at) VALUES (?, ?, ?, 'running', ?, 'sha256:id', ?, 'linux/amd64', ?)`,
			svcKey, appKey, appName, img, dig, now)
		if err != nil {
			t.Fatal(err)
		}
	}

	start := time.Now()
	chk.runCheck(ctx, "manual", "h1", true)
	elapsed := time.Since(start)

	// Serial execution of 12 services * 2 calls * 45ms would take > 1080ms; concurrent across 3 registries finishes much faster.
	if maxTotal.Load() < 5 {
		t.Fatalf("expected cross-registry concurrency >= 5, got maxTotal=%d (elapsed=%v)", maxTotal.Load(), elapsed)
	}
	if maxDocker.Load() > maxPerRegistryConcurrency {
		t.Fatalf("expected docker.io concurrency <= %d, got %d", maxPerRegistryConcurrency, maxDocker.Load())
	}

	p := chk.CurrentProgress()
	if p.Running || p.Phase != "done" || p.Completed != 12 || p.Total != 12 || len(p.CompletedServiceKeys) != 12 {
		t.Fatalf("unexpected final progress: %+v", p)
	}

	ov, err := chk.Overview(ctx, "h1", i18n.ZhCN)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(ov.Normal) != 12 {
		t.Fatalf("expected all 12 services to report version updates in Normal group, got %d (failed=%+v)", len(ov.Normal), ov.Failed)
	}
}

func TestStaleCurrentTagsClearedWhenContainerDowngradedExternally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"org/demo","tags":["0.6.0","0.8.5"]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/manifests/0.6.0") {
			w.Header().Set("Docker-Content-Digest", "sha256:dig060")
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfg060"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/manifests/0.8.5") {
			w.Header().Set("Docker-Content-Digest", "sha256:dig085")
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			_, _ = w.Write([]byte(`{"config":{"digest":"sha256:cfg085"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	chk, st := newTestChecker(t, map[string]string{
		"ghcr.io": srv.URL,
	})
	ctx := context.Background()
	now := time.Now().Unix()

	_, err := st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', 'Router', 'docker', 'ssh', 'online', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h1/standalone/demo', 'h1', 'demo', 'standalone', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟该容器此前曾升级到 0.8.5 导致 current_tags_json 残留 ["0.8.5"]，随后在外部被重建为 0.6.0（且本地摘要未命中任何远端标签）
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, current_tags_json, platform, updated_at)
		VALUES ('h1/standalone/demo/main', 'h1/standalone/demo', 'main', 'running', 'ghcr.io/org/demo:0.6.0', 'sha256:customid', 'sha256:customdig', '["0.8.5"]', 'linux/amd64', ?)`, now)
	if err != nil {
		t.Fatal(err)
	}

	chk.runCheck(ctx, "manual", "h1", true)

	var curTagsRaw string
	if err := st.DB.QueryRowContext(ctx, `SELECT COALESCE(current_tags_json,'') FROM service WHERE key='h1/standalone/demo/main'`).Scan(&curTagsRaw); err != nil {
		t.Fatal(err)
	}
	if curTagsRaw != "" {
		t.Fatalf("expected stale current_tags_json cleared to NULL, got %q", curTagsRaw)
	}

	ov, err := chk.Overview(ctx, "h1", i18n.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Caution) != 1 {
		t.Fatalf("expected 1 caution card (0.6.0 -> 0.8.5), got %d", len(ov.Caution))
	}
	card := ov.Caution[0]
	if card.SummaryCurrent != "0.6.0" || card.SummaryTarget != "0.8.5" {
		t.Fatalf("expected 0.6.0 -> 0.8.5, got %q -> %q", card.SummaryCurrent, card.SummaryTarget)
	}
}

func TestOverview_GHCRUpstream404AndForeverIgnoreSuppressesFailedBanner(t *testing.T) {
	var ghcrURL string
	ghcrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="ghcr.io",scope="repository:su-kaka/gcli2api:pull"`, ghcrURL))
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ghcrSrv.Close()
	ghcrURL = ghcrSrv.URL

	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ghSrv.Close()

	chk, st := newTestChecker(t, map[string]string{
		"ghcr.io":    ghcrSrv.URL,
		"github.com": ghSrv.URL,
	})
	ctx := context.Background()
	now := time.Now().Unix()

	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', 'NAS', 'synology_dsm', 'dsm_api', 'online', ?)`, now)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h1/dsm_project/gcli2api', 'h1', 'gcli2api', 'dsm_project', ?)`, now)
	// 模拟群晖 dsm_api 模式（RepoDigest 为空，仅有 ImageID）
	_, _ = st.DB.ExecContext(ctx, `
		INSERT INTO service(key, app_key, name, state, image_ref, image_id, repo_digest, platform, updated_at)
		VALUES ('h1/dsm_project/gcli2api/gcli2api', 'h1/dsm_project/gcli2api', 'gcli2api', 'running', 'ghcr.io/su-kaka/gcli2api:master-ecc207f', 'sha256:1111111111111111111111111111111111111111111111111111111111111111', '', 'linux/amd64', ?)`, now)

	chk.runCheck(ctx, "manual", "h1", true)

	ov, err := chk.Overview(ctx, "h1", i18n.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Failed) != 1 || ov.Failed[0].ErrorKind != string(registry.ErrNotFound) {
		t.Fatalf("expected 1 failed item with errorKind=not_found, got %+v", ov.Failed)
	}

	// 开启永久忽略后，顶部 Failed 列表不再包含该服务，而下方 NoUpdate 卡片处于 Ignored 状态
	if err := chk.ign.Set(ctx, []string{"h1/dsm_project/gcli2api/gcli2api"}, "forever", ""); err != nil {
		t.Fatal(err)
	}
	ovMuted, err := chk.Overview(ctx, "h1", i18n.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	if len(ovMuted.Failed) != 0 {
		t.Fatalf("expected 0 failed items after forever ignore, got %+v", ovMuted.Failed)
	}
	if len(ovMuted.NoUpdate) != 1 || !ovMuted.NoUpdate[0].Ignored {
		t.Fatalf("expected NoUpdate card marked Ignored=true, got %+v", ovMuted.NoUpdate)
	}
}
