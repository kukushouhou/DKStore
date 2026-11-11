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
		Progress:       platform.PullProgress{Phase: "precheck", StatusText: "Preparing", Percent: 5},
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

	// Emit a pulling event with Percent: 0 -> must stay monotonic at 5%
	tracker.handleProgress(ctx, &job, platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Pulling fs layer",
		Percent:    0,
		TotalLayers: 1,
		Layers:     []platform.LayerProgress{{ID: "a1b2c3d4", Status: "waiting", Percent: 0}},
		LogChunk:   "a1b2c3d4: Pulling fs layer\r\n",
	})
	if job.Progress.Percent != 5 {
		t.Fatalf("expected monotonic percent 5 (not dropping to 0), got %d", job.Progress.Percent)
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

	// Fail the job -> error must be appended in ANSI red to Logs and persisted to SQLite
	tracker.fail(job, "pull_failed", "unauthorized: authentication required")

	persisted, err := up.GetJob(ctx, job.ID, i18n.EnUS)
	if err != nil {
		t.Fatalf("GetJob after failure: %v", err)
	}
	if persisted.Status != "failed" || !strings.Contains(persisted.Logs, "\x1b[31m✖ ERROR:") || !strings.Contains(persisted.Logs, "unauthorized") {
		t.Fatalf("expected ANSI error persisted in logs_text, got: %q", persisted.Logs)
	}

	// Drain SSE events to confirm LogChunk was broadcast
	sawChunk := false
	for len(subCh) > 0 {
		ev := <-subCh
		if strings.Contains(ev.LogChunk, "✖ ERROR:") {
			sawChunk = true
		}
	}
	if !sawChunk {
		t.Fatal("expected SSE broadcast to include terminal error LogChunk")
	}
}
