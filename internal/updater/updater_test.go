package updater

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/inventory"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/secret"
	"github.com/kukushouhou/DKStore/internal/store"
)

func newTestUpdater(t *testing.T, baseURLOverride map[string]string) (*Service, *store.Store) {
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
	return New(slog.Default(), st, hm, inv, reg), st
}

func TestVerifyRollback_LatestWithoutExactTag_Ineligible(t *testing.T) {
	up, _ := newTestUpdater(t, nil)
	ctx := context.Background()

	job := Job{
		ID:               "up_test_latest_no_tag",
		Kind:             "upgrade",
		HostID:           "local",
		HostName:         "Local",
		AppKey:           "local:standalone:app1",
		AppName:          "app1",
		ServiceKey:       "local:standalone:app1:main",
		ServiceName:      "main",
		Source:           "standalone",
		Status:           "succeeded",
		Phase:            "done",
		OldImageRef:      "nginx:latest",
		OldRepoDigest:    "sha256:aaaa1111",
		OldVersion:       "",
		TargetImageRef:   "nginx:latest",
		TargetRepoDigest: "sha256:bbbb2222",
		RollbackEligible: false,
		RollbackReason:   "no_exact_version_tag",
		StartedAt:        1700000000,
	}
	if err := up.insertJob(ctx, job); err != nil {
		t.Fatalf("insertJob: %v", err)
	}

	res, err := up.VerifyRollback(ctx, job.ID, i18n.ZhCN)
	if err != nil {
		t.Fatalf("VerifyRollback: %v", err)
	}
	if res.Eligible {
		t.Fatalf("expected Eligible=false for latest without exact version tag")
	}
	if res.ReasonCode != "no_exact_version_tag" {
		t.Fatalf("expected reasonCode=no_exact_version_tag, got %s", res.ReasonCode)
	}
}

func TestVerifyRollback_RemoteTagMatchesVsOverwritten(t *testing.T) {
	currentRemoteDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", currentRemoteDigest)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer srv.Close()

	up, _ := newTestUpdater(t, map[string]string{
		"ghcr.io": srv.URL,
	})

	ctx := context.Background()
	job := Job{
		ID:               "up_test_exact_tag",
		Kind:             "upgrade",
		HostID:           "local",
		HostName:         "Local",
		AppKey:           "local:compose:myapp",
		AppName:          "myapp",
		ServiceKey:       "local:compose:myapp:web",
		ServiceName:      "web",
		Source:           "compose",
		Status:           "succeeded",
		Phase:            "done",
		OldImageRef:      "ghcr.io/myorg/myapp:latest",
		OldRepoDigest:    currentRemoteDigest,
		OldVersion:       "1.4.2",
		OldMatchedTags:   []string{"1.4.2"},
		TargetImageRef:   "ghcr.io/myorg/myapp:1.5.0",
		TargetVersion:    "1.5.0",
		RollbackEligible: true,
		RollbackRef:      "ghcr.io/myorg/myapp:1.4.2",
		StartedAt:        1700000100,
	}
	if err := up.insertJob(ctx, job); err != nil {
		t.Fatalf("insertJob: %v", err)
	}

	// 1. Remote tag 1.4.2 still points to OldRepoDigest -> Eligible = true
	res, err := up.VerifyRollback(ctx, job.ID, i18n.EnUS)
	if err != nil {
		t.Fatalf("VerifyRollback: %v", err)
	}
	if !res.Eligible {
		t.Fatalf("expected Eligible=true when remote tag matches old digest, got reason=%s (%s)", res.ReasonCode, res.ReasonText)
	}
	if res.RollbackRef != "ghcr.io/myorg/myapp:1.4.2" {
		t.Fatalf("unexpected rollbackRef: %s", res.RollbackRef)
	}

	// 2. Now simulate upstream overwriting tag 1.4.2 with a different digest -> Eligible = false (tag_overwritten)
	currentRemoteDigest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	res2, err := up.VerifyRollback(ctx, job.ID, i18n.EnUS)
	if err != nil {
		t.Fatalf("VerifyRollback after overwrite: %v", err)
	}
	if res2.Eligible {
		t.Fatalf("expected Eligible=false when remote tag was overwritten and local image missing")
	}
	if res2.ReasonCode != "tag_overwritten" {
		t.Fatalf("expected reasonCode=tag_overwritten, got %s", res2.ReasonCode)
	}
}

func TestVerifyRollback_EmptyRepoDigestRequiresConfigDigestMatch(t *testing.T) {
	configDigest := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:2222222222222222222222222222222222222222222222222222222222222222")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"}}`))
	}))
	defer srv.Close()

	up, _ := newTestUpdater(t, map[string]string{
		"ghcr.io": srv.URL,
	})
	ctx := context.Background()

	// 1. OldRepoDigest is empty and OldImageID matches remote ConfigDigest -> Eligible = true
	jobMatch := Job{
		ID:               "up_test_empty_digest_match",
		Kind:             "upgrade",
		HostID:           "local",
		HostName:         "Local",
		AppKey:           "local:dsm_project:myapp",
		AppName:          "myapp",
		ServiceKey:       "local:dsm_project:myapp:web",
		ServiceName:      "web",
		Source:           "dsm_project",
		Status:           "succeeded",
		Phase:            "done",
		OldImageRef:      "ghcr.io/myorg/myapp:1.4.2",
		OldImageID:       configDigest,
		OldRepoDigest:    "",
		OldVersion:       "1.4.2",
		TargetImageRef:   "ghcr.io/myorg/myapp:1.4.2",
		RollbackEligible: true,
		RollbackRef:      "ghcr.io/myorg/myapp:1.4.2",
		StartedAt:        1700000200,
	}
	if err := up.insertJob(ctx, jobMatch); err != nil {
		t.Fatalf("insertJob: %v", err)
	}
	resMatch, err := up.VerifyRollback(ctx, jobMatch.ID, i18n.ZhCN)
	if err != nil {
		t.Fatalf("VerifyRollback match: %v", err)
	}
	if !resMatch.Eligible {
		t.Fatalf("expected Eligible=true when OldImageID matches remote ConfigDigest, got %+v", resMatch)
	}

	// 2. OldRepoDigest is empty and OldImageID does NOT match remote ConfigDigest -> Eligible = false
	jobMismatch := jobMatch
	jobMismatch.ID = "up_test_empty_digest_mismatch"
	jobMismatch.OldImageID = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := up.insertJob(ctx, jobMismatch); err != nil {
		t.Fatalf("insertJob mismatch: %v", err)
	}
	resMismatch, err := up.VerifyRollback(ctx, jobMismatch.ID, i18n.ZhCN)
	if err != nil {
		t.Fatalf("VerifyRollback mismatch: %v", err)
	}
	if resMismatch.Eligible {
		t.Fatalf("expected Eligible=false when OldRepoDigest is empty and OldImageID mismatches ConfigDigest")
	}
	if resMismatch.ReasonCode != "tag_overwritten" {
		t.Fatalf("expected reasonCode=tag_overwritten, got %s", resMismatch.ReasonCode)
	}
}

func TestNew_RecoversInterruptedJobs(t *testing.T) {
	up, st := newTestUpdater(t, nil)
	ctx := context.Background()

	job := Job{
		ID:             "up_test_interrupted",
		Kind:           "upgrade",
		HostID:         "local",
		HostName:       "Local",
		AppKey:         "local:standalone:app1",
		AppName:        "app1",
		ServiceKey:     "local:standalone:app1:main",
		ServiceName:    "main",
		Source:         "standalone",
		Status:         "running",
		Phase:          "pulling",
		OldImageRef:    "nginx:1.26",
		TargetImageRef: "nginx:1.27",
		StartedAt:      1700000300,
	}
	if err := up.insertJob(ctx, job); err != nil {
		t.Fatalf("insertJob: %v", err)
	}

	// Re-initialize updater service to simulate restart recovery
	up2 := New(slog.Default(), st, up.hosts, up.inv, up.reg)
	got, err := up2.GetJob(ctx, job.ID, i18n.EnUS)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != "failed" || got.ErrorCode != "interrupted" {
		t.Fatalf("expected status=failed and errorCode=interrupted, got status=%s code=%s", got.Status, got.ErrorCode)
	}
}

func TestJobProgressTracker_MonotonicPercentLiveMemoryLogsAndErrorFormatting(t *testing.T) {
	up, _ := newTestUpdater(t, nil)
	ctx := context.Background()

	subCh, unsub := up.Subscribe()
	defer unsub()

	job := Job{
		ID:             "up_test_live_stream",
		Kind:           "upgrade",
		HostID:         "local",
		HostName:       "Local",
		AppKey:         "local/dsm_project/demo",
		AppName:        "demo",
		ServiceKey:     "local/dsm_project/demo/app",
		ServiceName:    "app",
		Source:         "dsm_project",
		Status:         "running",
		Phase:          "precheck",
		Progress:       platform.PullProgress{Phase: "precheck", StatusText: "Preparing", Percent: 0},
		OldImageRef:    "ghcr.io/org/demo:1.0",
		TargetImageRef: "ghcr.io/org/demo:1.1",
		StartedAt:      1700000400,
	}
	initHdr := formatJobHeader(job)
	job.Logs = initHdr
	job.LogOffset = len(initHdr)
	if err := up.insertJob(ctx, job); err != nil {
		t.Fatalf("insertJob: %v", err)
	}

	tracker := newJobProgressTracker(up, &job)
	if job.Progress.Percent != 0 {
		t.Fatalf("expected initial precheck percent to stay at 0, got %d", job.Progress.Percent)
	}

	// Emit a pulling event with Percent: 0 while waiting -> must remain honestly at 0%
	tracker.handleProgress(ctx, &job, platform.PullProgress{
		Phase:       "pulling",
		StatusText:  "Pulling fs layer",
		Percent:     0,
		TotalLayers: 1,
		Layers:      []platform.LayerProgress{{ID: "a1b2c3d4", Status: "waiting", Percent: 0}},
		LogChunk:    "a1b2c3d4: Pulling fs layer\r\n",
	})
	if job.Progress.Percent != 0 {
		t.Fatalf("expected 0 percent before any download starts, got %d", job.Progress.Percent)
	}

	// Verify GetJob returns live in-memory accumulated logs while running
	liveJob, err := up.GetJob(ctx, job.ID, i18n.EnUS)
	if err != nil {
		t.Fatalf("GetJob while active: %v", err)
	}
	if !strings.Contains(liveJob.Logs, "a1b2c3d4: Pulling fs layer") || liveJob.LogOffset != len(liveJob.Logs) {
		t.Fatalf("expected live accumulated logs & offset, got logs=%q offset=%d", liveJob.Logs, liveJob.LogOffset)
	}

	// Advance to 45%, then try to emit 20% -> must stay at 45%
	tracker.handleProgress(ctx, &job, platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Downloading",
		Percent:    45,
		Layers:     []platform.LayerProgress{{ID: "a1b2c3d4", Status: "downloading", Percent: 50}},
		LogChunk:   "\r\x1b[2Ka1b2c3d4: Downloading 50%",
	})
	tracker.handleProgress(ctx, &job, platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Downloading",
		Percent:    20,
	})
	if job.Progress.Percent != 45 {
		t.Fatalf("expected monotonic percent 45, got %d", job.Progress.Percent)
	}

	// Fail the job -> humanized error must be appended in ANSI red to Logs and persisted to SQLite
	tracker.fail(job, "pull_failed", "unauthorized: authentication required")

	persisted, err := up.GetJob(ctx, job.ID, i18n.EnUS)
	if err != nil {
		t.Fatalf("GetJob after failure: %v", err)
	}
	if persisted.Status != "failed" || !strings.Contains(persisted.Logs, "\x1b[31m✖ ") || !strings.Contains(persisted.ErrorMsg, "authentication required") {
		t.Fatalf("expected ANSI error persisted in logs_text and localized ErrorMsg, got logs=%q err=%q", persisted.Logs, persisted.ErrorMsg)
	}

	// Drain SSE events to confirm LogChunk was broadcast
	sawChunk := false
	for len(subCh) > 0 {
		ev := <-subCh
		if strings.Contains(ev.LogChunk, "✖ ") {
			sawChunk = true
		}
	}
	if !sawChunk {
		t.Fatal("expected SSE broadcast to include terminal error LogChunk")
	}
}

func TestNormalizeOldDockerZstdError(t *testing.T) {
	cases := []struct {
		ver  string
		want bool
	}{
		{"19.03.13", true},
		{"20.10.6", true},
		{"20.10.9", true},
		{"20.10.10", false},
		{"24.0.7", false},
		{"27.3.1", false},
		{"Container Manager (DSM WebAPI)", false},
	}
	for _, c := range cases {
		if got := isDockerBeforeZstd(c.ver); got != c.want {
			t.Errorf("isDockerBeforeZstd(%q) = %v, want %v", c.ver, got, c.want)
		}
	}

	raw := "failed to register layer: ApplyLayer exit status 1 stdout:  stderr: archive/tar: invalid tar header"
	code, msg := normalizeUpdateError("pull_failed", raw, &host.Host{
		Info: &platform.HostInfo{DockerVersion: "20.10.6"},
	})
	if code != "docker_old_zstd" {
		t.Fatalf("code = %q, want docker_old_zstd", code)
	}
	zh := i18n.FormatUpdateError(i18n.ZhCN, code, msg)
	en := i18n.FormatUpdateError(i18n.EnUS, code, msg)
	if !strings.Contains(zh, "20.10.6") || !strings.Contains(zh, "zstd") {
		t.Fatalf("unexpected zh error: %q", zh)
	}
	if !strings.Contains(en, "20.10.6") || !strings.Contains(en, "zstd") {
		t.Fatalf("unexpected en error: %q", en)
	}

	// Legacy unencoded ApplyLayer error stored in SQLite should also localize cleanly
	legacyZh := i18n.FormatUpdateError(i18n.ZhCN, "pull_failed", raw)
	if !strings.Contains(legacyZh, "zstd") || strings.Contains(legacyZh, "invalid tar header") {
		t.Fatalf("unexpected legacy zh error: %q", legacyZh)
	}
}

func TestFailedJobErrorCodeFrozenAcrossSubsequentMirrorChanges(t *testing.T) {
	up, st := newTestUpdater(t, nil)
	ctx := context.Background()

	_, err := st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h-router', '软路由模拟器', 'docker', 'ssh', 'online', 1700000000)`)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 未配置镜像源时任务失败：收尾时定性为 pull_registry_unreachable
	job1 := Job{
		ID:             "job-unconfigured-1",
		Kind:           "upgrade",
		HostID:         "h-router",
		HostName:       "软路由模拟器",
		AppKey:         "h-router/standalone/uptime-kuma",
		AppName:        "uptime-kuma",
		ServiceKey:     "h-router/standalone/uptime-kuma/main",
		ServiceName:    "main",
		Source:         "standalone",
		Status:         "running",
		Phase:          "pulling",
		OldImageRef:    "louislam/uptime-kuma:2.5.11",
		TargetImageRef: "louislam/uptime-kuma:2.5.12",
		StartedAt:      1700000010,
	}
	if err := up.insertJob(ctx, job1); err != nil {
		t.Fatal(err)
	}
	tracker1 := newJobProgressTracker(up, &job1)
	tracker1.fail(job1, "pull_failed", `Get "https://registry-1.docker.io/v2/": dial tcp 199.59.148.20:443: connection refused`)

	// 2. 用户随后在该设备上保存了镜像源
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO host_mirror(host_id, pairs_json, updated_at)
		VALUES ('h-router', '[{"source":"docker.io","urls":["https://docker.1ms.run"]}]', 1700000020)`)
	if err != nil {
		t.Fatal(err)
	}

	// 3. 重新通过 GetJob 与 ListJobs 查询旧失败记录，必须保持当时的诊断（无镜像源异常提示、无 mirror 动作）
	got1, err := up.GetJob(ctx, job1.ID, i18n.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	if got1.ErrorCode != "pull_registry_unreachable" || got1.ErrorAction != "" || got1.ErrorDetail != "请检查设备网络连接，或稍后重试" {
		t.Fatalf("expected job1 error frozen after mirror added, got code=%q action=%q detail=%q", got1.ErrorCode, got1.ErrorAction, got1.ErrorDetail)
	}
	got1En, err := up.GetJob(ctx, job1.ID, i18n.EnUS)
	if err != nil {
		t.Fatal(err)
	}
	if got1En.ErrorAction != "" || !strings.Contains(got1En.ErrorDetail, "try again later") {
		t.Fatalf("expected English localization to remain mirror-free, got %+v", got1En)
	}

	list, err := up.ListJobs(ctx, 10, i18n.ZhCN)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListJobs err=%v len=%d", err, len(list))
	}
	if list[0].ErrorAction != "" || list[0].ErrorDetail != "请检查设备网络连接，或稍后重试" {
		t.Fatalf("expected ListJobs to keep frozen error without querying current host mirrors, got %+v", list[0])
	}

	// 4. 发起携带镜像源的新任务仍失败时，新任务定性为 pull_registry_unreachable_mirror
	job2 := job1
	job2.ID = "job-configured-2"
	job2.StartedAt = 1700000030
	job2.configuredMirrors = []string{"https://docker.1ms.run"}
	if err := up.insertJob(ctx, job2); err != nil {
		t.Fatal(err)
	}
	tracker2 := newJobProgressTracker(up, &job2)
	tracker2.fail(job2, "pull_failed", `Get "https://docker.1ms.run/v2/": dial tcp: connection refused`)

	got2, err := up.GetJob(ctx, job2.ID, i18n.ZhCN)
	if err != nil {
		t.Fatal(err)
	}
	if got2.ErrorCode != "pull_registry_unreachable_mirror" || got2.ErrorAction != "mirror" || got2.ErrorDetail != "请检查设备网络连接，也可能是已配置的镜像源异常" {
		t.Fatalf("expected job2 to record mirror failure code, got code=%q action=%q detail=%q", got2.ErrorCode, got2.ErrorAction, got2.ErrorDetail)
	}
}

func TestStructuredResolvedTagRollbackAndLegacyBackfill(t *testing.T) {
	up, st := newTestUpdater(t, nil)
	ctx := context.Background()

	_, err := st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', 'NAS', 'synology', 'dsm_api', 'online', 1700000000)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h1/dsm_project/iptv', 'h1', 'iptv', 'dsm_project', 1700000000)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO service(key, app_key, name, image_ref, image_id, platform, current_tags_json, updated_at)
		VALUES ('h1/dsm_project/iptv/rtp2httpd', 'h1/dsm_project/iptv', 'rtp2httpd', 'ghcr.nju.edu.cn/stackia/rtp2httpd:latest', 'sha256:573910c7', 'linux/amd64', '["latest"]', 1700000000)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO finding(service_key, kind, category, current_version, current_resolved_tag, target_version, target_resolved_tag, target_ref)
		VALUES ('h1/dsm_project/iptv/rtp2httpd', 'digest_update', 'normal', 'latest (3.8.3)', '3.8.3', 'latest (3.17.2)', '3.17.2', 'ghcr.io/stackia/rtp2httpd:latest')`)
	if err != nil {
		t.Fatal(err)
	}

	j1, _, _, err := up.prepareUpgradeJob(ctx, UpdateItemRequest{ServiceKey: "h1/dsm_project/iptv/rtp2httpd"}, i18n.ZhCN)
	if err != nil {
		t.Fatalf("prepareUpgradeJob: %v", err)
	}
	if !j1.RollbackEligible || j1.RollbackRef != "ghcr.nju.edu.cn/stackia/rtp2httpd:3.8.3" || j1.OldResolvedTag != "3.8.3" {
		t.Fatalf("expected structured rollback eligible to :3.8.3, got eligible=%v ref=%q oldResolved=%q", j1.RollbackEligible, j1.RollbackRef, j1.OldResolvedTag)
	}

	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO service(key, app_key, name, image_ref, image_id, repo_digest, platform, current_tags_json, updated_at)
		VALUES ('h1/dsm_project/iptv/ha', 'h1/dsm_project/iptv', 'ha', 'homeassistant/home-assistant:latest', 'sha256:13894a', 'sha256:fccef878', 'linux/amd64', '["latest"]', 1700000000)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO finding(service_key, kind, category, current_version, current_resolved_tag, target_version, target_resolved_tag, target_ref)
		VALUES ('h1/dsm_project/iptv/ha', 'digest_update', 'normal', 'latest (fccef8)', '', 'latest (2026.11.1)', '2026.11.1', 'homeassistant/home-assistant:latest')`)
	if err != nil {
		t.Fatal(err)
	}
	j2, _, _, err := up.prepareUpgradeJob(ctx, UpdateItemRequest{ServiceKey: "h1/dsm_project/iptv/ha"}, i18n.ZhCN)
	if err != nil {
		t.Fatalf("prepareUpgradeJob ha: %v", err)
	}
	if j2.RollbackEligible || j2.RollbackReason != "no_exact_version_tag" {
		t.Fatalf("expected floating latest without resolved tag to be ineligible, got eligible=%v reason=%q", j2.RollbackEligible, j2.RollbackReason)
	}

	_, err = st.DB.ExecContext(ctx, `
		INSERT INTO update_job(
			id, kind, host_id, host_name, app_key, app_name, service_key, service_name, source,
			status, phase, old_image_ref, old_version, target_image_ref, target_version,
			rollback_eligible, rollback_ref, rollback_reason, started_at, finished_at
		) VALUES (
			'up_legacy_rtp', 'upgrade', 'h1', 'NAS', 'h1/dsm_project/iptv', 'iptv', 'h1/dsm_project/iptv/rtp2httpd', 'rtp2httpd', 'dsm_project',
			'succeeded', 'done', 'ghcr.nju.edu.cn/stackia/rtp2httpd:latest', 'latest (3.8.3)',
			'ghcr.nju.edu.cn/stackia/rtp2httpd:latest', 'latest (3.17.2)',
			0, '', 'no_exact_version_tag', 1700000500, 1700000600
		)`)
	if err != nil {
		t.Fatal(err)
	}
	up.backfillLegacyResolvedTags()
	repaired, err := up.GetJob(ctx, "up_legacy_rtp", i18n.ZhCN)
	if err != nil {
		t.Fatalf("GetJob repaired: %v", err)
	}
	if !repaired.RollbackEligible || repaired.RollbackRef != "ghcr.nju.edu.cn/stackia/rtp2httpd:3.8.3" || repaired.OldResolvedTag != "3.8.3" {
		t.Fatalf("expected legacy row repaired to eligible :3.8.3, got %+v", repaired)
	}
}

func TestClearJobs_FailedVsAll(t *testing.T) {
	up, _ := newTestUpdater(t, nil)
	ctx := context.Background()

	mk := func(id, status string, ts int64) Job {
		return Job{
			ID:             id,
			Kind:           "upgrade",
			HostID:         "h1",
			HostName:       "NAS",
			AppKey:         "app/" + id,
			AppName:        id,
			ServiceKey:     "svc/" + id,
			ServiceName:    "main",
			Source:         "standalone",
			Status:         status,
			Phase:          "done",
			OldImageRef:    "nginx:1.26",
			TargetImageRef: "nginx:1.27",
			StartedAt:      ts,
		}
	}
	_ = up.insertJob(ctx, mk("j_ok", "succeeded", 100))
	_ = up.insertJob(ctx, mk("j_fail1", "failed", 200))
	_ = up.insertJob(ctx, mk("j_fail2", "failed", 300))
	_ = up.insertJob(ctx, mk("j_run", "running", 400))

	nFailed, err := up.ClearJobs(ctx, "failed")
	if err != nil || nFailed != 2 {
		t.Fatalf("ClearJobs(failed) = %d, %v; want 2, nil", nFailed, err)
	}
	rem1, _ := up.ListJobs(ctx, 50, i18n.ZhCN)
	if len(rem1) != 2 {
		t.Fatalf("expected 2 remaining jobs (succeeded + running), got %d", len(rem1))
	}

	nAll, err := up.ClearJobs(ctx, "all")
	if err != nil || nAll != 1 {
		t.Fatalf("ClearJobs(all) = %d, %v; want 1 (preserving running), nil", nAll, err)
	}
	rem2, _ := up.ListJobs(ctx, 50, i18n.ZhCN)
	if len(rem2) != 1 || rem2[0].ID != "j_run" {
		t.Fatalf("expected only running job preserved, got %+v", rem2)
	}
}

func TestRetryJob_PreservesTargetAndRefreshesCurrentImage(t *testing.T) {
	up, st := newTestUpdater(t, nil)
	ctx := context.Background()

	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h_retry', '软路由模拟器', 'docker', 'ssh', 'online', 1700000000)`)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h_retry/standalone/ha', 'h_retry', 'home-assistant', 'standalone', 1700000000)`)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, image_ref, image_id, repo_digest, updated_at) VALUES ('h_retry/standalone/ha/main', 'h_retry/standalone/ha', 'main', 'homeassistant/home-assistant:latest', 'sha256:curimg999', 'sha256:curdig999', 1700000000)`)

	failedOrig := Job{
		ID:                "up_failed_eof",
		Kind:              "upgrade",
		HostID:            "h_retry",
		HostName:          "软路由模拟器",
		AppKey:            "h_retry/standalone/ha",
		AppName:           "home-assistant",
		ServiceKey:        "h_retry/standalone/ha/main",
		ServiceName:       "main",
		Source:            "standalone",
		Status:            "failed",
		Phase:             "done",
		OldImageRef:       "homeassistant/home-assistant:latest",
		OldImageID:        "sha256:oldimg111",
		OldVersion:        "latest (fccef8)",
		TargetImageRef:    "homeassistant/home-assistant:latest",
		TargetVersion:     "latest (2026.11.1)",
		TargetResolvedTag: "2026.11.1",
		StartedAt:         1700000100,
	}
	if err := up.insertJob(ctx, failedOrig); err != nil {
		t.Fatal(err)
	}

	subCh, unsub := up.Subscribe()
	defer unsub()

	retried, err := up.RetryJob(ctx, failedOrig.ID, i18n.ZhCN)
	if err != nil {
		t.Fatalf("RetryJob failed: %v", err)
	}
	if retried.ID == failedOrig.ID || retried.TargetImageRef != "homeassistant/home-assistant:latest" || retried.TargetVersion != "latest (2026.11.1)" {
		t.Fatalf("unexpected retried job: %+v", retried)
	}
	if retried.OldImageID != "sha256:curimg999" {
		t.Fatalf("expected refreshed OldImageID sha256:curimg999, got %q", retried.OldImageID)
	}

	// 等待后台协程完成收尾，避免 TempDir 关闭竞态
	for ev := range subCh {
		if ev.ID == retried.ID && (ev.Status == "failed" || ev.Status == "succeeded") {
			break
		}
	}
}

func TestListAndGetJobs_OnlyLatestFailedPerServiceIsRetryable(t *testing.T) {
	up, _ := newTestUpdater(t, nil)
	ctx := context.Background()

	mk := func(id, status string, ts int64) Job {
		return Job{
			ID:             id,
			Kind:           "upgrade",
			HostID:         "h1",
			HostName:       "软路由模拟器",
			AppKey:         "h1/standalone/home-assistant",
			AppName:        "home-assistant",
			ServiceKey:     "h1/standalone/home-assistant/main",
			ServiceName:    "main",
			Source:         "standalone",
			Status:         status,
			Phase:          "done",
			OldImageRef:    "homeassistant/home-assistant:latest",
			TargetImageRef: "homeassistant/home-assistant:latest",
			StartedAt:      ts,
		}
	}

	if err := up.insertJob(ctx, mk("j_old_fail", "failed", 1000)); err != nil {
		t.Fatal(err)
	}
	if err := up.insertJob(ctx, mk("j_new_fail", "failed", 2000)); err != nil {
		t.Fatal(err)
	}

	list1, err := up.ListJobs(ctx, 50, i18n.ZhCN)
	if err != nil || len(list1) != 2 {
		t.Fatalf("ListJobs len=%d err=%v", len(list1), err)
	}
	if !list1[0].Retryable || list1[0].ID != "j_new_fail" {
		t.Fatalf("expected newest failed job to be retryable, got %+v", list1[0])
	}
	if list1[1].Retryable || list1[1].ID != "j_old_fail" {
		t.Fatalf("expected older failed job to NOT be retryable, got %+v", list1[1])
	}

	gotOld, err := up.GetJob(ctx, "j_old_fail", i18n.ZhCN)
	if err != nil || gotOld.Retryable {
		t.Fatalf("expected GetJob(j_old_fail).Retryable=false, got %+v err=%v", gotOld, err)
	}
	gotNew, err := up.GetJob(ctx, "j_new_fail", i18n.ZhCN)
	if err != nil || !gotNew.Retryable {
		t.Fatalf("expected GetJob(j_new_fail).Retryable=true, got %+v err=%v", gotNew, err)
	}

	// 一旦同一服务后续已有成功记录，此前的所有失败记录均不再可重试
	if err := up.insertJob(ctx, mk("j_later_ok", "succeeded", 3000)); err != nil {
		t.Fatal(err)
	}
	list2, err := up.ListJobs(ctx, 50, i18n.ZhCN)
	if err != nil || len(list2) != 3 {
		t.Fatalf("ListJobs after success len=%d err=%v", len(list2), err)
	}
	for _, item := range list2 {
		if item.Retryable {
			t.Fatalf("expected no retryable jobs after later success, got %+v", item)
		}
	}
	gotNewAfterOK, err := up.GetJob(ctx, "j_new_fail", i18n.ZhCN)
	if err != nil || gotNewAfterOK.Retryable {
		t.Fatalf("expected j_new_fail Retryable=false after later success, got %+v err=%v", gotNewAfterOK, err)
	}
}
