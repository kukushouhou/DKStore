package updater

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/inventory"
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
