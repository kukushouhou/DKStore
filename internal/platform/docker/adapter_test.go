package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseComposeImagesAndDotEnv(t *testing.T) {
	env := parseDotEnv([]byte(`
# comment
APP_TAG=1.27.2-alpine
EMPTY_VAR=
`))
	yml := []byte(`
services:
  web:
    image: nginx:${APP_TAG:-1.26.0}
  db:
    image: postgres:${PG_TAG:-16.4}
  cache:
    image: redis:${EMPTY_VAR:-7.2-alpine}
`)
	got := parseComposeImages(yml, env)
	if got["web"] != "nginx:1.27.2-alpine" {
		t.Fatalf("web = %q", got["web"])
	}
	if got["db"] != "postgres:16.4" {
		t.Fatalf("db = %q", got["db"])
	}
	if got["cache"] != "redis:7.2-alpine" {
		t.Fatalf("cache = %q", got["cache"])
	}
}

func TestIsVendorManagedContainer(t *testing.T) {
	// User-created Compose project on fnOS (/vol1/1000/docker/memos) with watchcow label must remain updatable
	if isVendorManagedContainer(map[string]string{
		"com.docker.compose.project":             "memos",
		"com.docker.compose.project.working_dir": "/vol1/1000/docker/memos",
		"watchcow.enable":                        "true",
	}, []string{"PATH=/usr/bin"}, []string{"/vol1/1000/docker/memos/data"}) {
		t.Fatal("user compose project in /vol1/1000/docker must NOT be vendor-managed")
	}

	// fnOS App Center package (/var/apps/immich/target/docker) must be vendor-managed
	if !isVendorManagedContainer(map[string]string{
		"com.docker.compose.project":             "immich",
		"com.docker.compose.project.working_dir": "/var/apps/immich/target/docker",
	}, nil, nil) {
		t.Fatal("/var/apps/* must be detected as vendor-managed")
	}

	// fnOS App Center resolved symlink (/vol1/@appcenter/plex/docker) must be vendor-managed
	if !isVendorManagedContainer(map[string]string{
		"com.docker.compose.project.working_dir": "/vol1/@appcenter/plex/docker",
	}, nil, nil) {
		t.Fatal("/vol1/@appcenter/* must be detected as vendor-managed")
	}

	// TRIM_APPDEST env var must be vendor-managed
	if !isVendorManagedContainer(nil, []string{"TRIM_APPDEST=/var/apps/jellyfin/target"}, nil) {
		t.Fatal("TRIM_APPDEST env must be detected as vendor-managed")
	}

	// User container mounting shared data folder (/var/apps/notepad/share/notes) must NOT be locked as vendor-managed
	if isVendorManagedContainer(nil, nil, []string{"/var/apps/notepad/share/notes"}) {
		t.Fatal("/var/apps/*/share/* user data mount must NOT lock user container")
	}
}

func TestListRuntimeAppsWithOptions_FastAndOneShotStats(t *testing.T) {
	var statsHits atomic.Int32
	tr := &fakeExecTransport{
		doHandler: func(path string) (*http.Response, error) {
			jsonResp := func(v any) (*http.Response, error) {
				b, _ := json.Marshal(v)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(b)),
				}, nil
			}
			if path == "/containers/json?all=1" {
				return jsonResp([]map[string]any{
					{
						"Id":     "c111111111111111",
						"Names":  []string{"/nginx-web"},
						"Image":  "nginx:1.27-alpine",
						"State":  "running",
						"Status": "Up 2 hours (healthy)",
					},
				})
			}
			if strings.Contains(path, "/stats?stream=false&one-shot=true") {
				hit := statsHits.Add(1)
				return jsonResp(map[string]any{
					"cpu_stats": map[string]any{
						"cpu_usage":        map[string]any{"total_usage": uint64(hit) * 50_000_000},
						"system_cpu_usage": uint64(hit) * 1_000_000_000,
						"online_cpus":      2,
					},
					"precpu_stats": map[string]any{},
					"memory_stats": map[string]any{
						"usage": uint64(64 * 1024 * 1024),
						"limit": uint64(1024 * 1024 * 1024),
					},
				})
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		},
	}

	ad := New("h1", tr)

	// 1. withStats=false skips /stats completely and marks StatsReady=false
	fastTrees, err := ad.ListRuntimeAppsWithOptions(context.Background(), false)
	if err != nil || len(fastTrees) != 1 {
		t.Fatalf("fast ListRuntimeAppsWithOptions failed: len=%d err=%v", len(fastTrees), err)
	}
	if fastTrees[0].StatsReady || statsHits.Load() != 0 {
		t.Fatalf("expected StatsReady=false and 0 stats hits, got StatsReady=%v hits=%d", fastTrees[0].StatsReady, statsHits.Load())
	}

	// 2. Cold withStats=true uses two one-shot samples (150ms apart) to compute CPU%
	coldTrees, err := ad.ListRuntimeAppsWithOptions(context.Background(), true)
	if err != nil || len(coldTrees) != 1 {
		t.Fatalf("cold ListRuntimeAppsWithOptions failed: err=%v", err)
	}
	if !coldTrees[0].StatsReady || statsHits.Load() != 2 || coldTrees[0].CPUPercent <= 0 {
		t.Fatalf("expected cold statsReady=true, hits=2, cpu>0; got ready=%v hits=%d cpu=%v", coldTrees[0].StatsReady, statsHits.Load(), coldTrees[0].CPUPercent)
	}

	// 3. Warm withStats=true reuses previous CPU sample in a SINGLE one-shot call (no sleep)
	warmTrees, err := ad.ListRuntimeAppsWithOptions(context.Background(), true)
	if err != nil || len(warmTrees) != 1 {
		t.Fatalf("warm ListRuntimeAppsWithOptions failed: err=%v", err)
	}
	if statsHits.Load() != 3 || warmTrees[0].CPUPercent <= 0 {
		t.Fatalf("expected warm call to add only 1 stats hit (total 3), got hits=%d cpu=%v", statsHits.Load(), warmTrees[0].CPUPercent)
	}
}

func TestParseOpenWrtRelease(t *testing.T) {
	istore := []byte("DISTRIB_ID='iStoreOS'\nDISTRIB_RELEASE='22.03.7'\nDISTRIB_DESCRIPTION='iStoreOS 22.03.7 2025050912'\n")
	if got := parseOpenWrtRelease(istore); got != "iStoreOS 22.03.7" {
		t.Fatalf("expected iStoreOS 22.03.7, got %q", got)
	}
	owrt := []byte("DISTRIB_ID='OpenWrt'\nDISTRIB_RELEASE='24.10.0'\nDISTRIB_DESCRIPTION='OpenWrt 24.10.0 r28427-6df0e3d02a'\n")
	if got := parseOpenWrtRelease(owrt); got != "OpenWrt 24.10.0" {
		t.Fatalf("expected OpenWrt 24.10.0, got %q", got)
	}
	snap := []byte("DISTRIB_ID='OpenWrt'\nDISTRIB_RELEASE='SNAPSHOT'\nDISTRIB_DESCRIPTION='OpenWrt SNAPSHOT r29000'\n")
	if got := parseOpenWrtRelease(snap); got != "OpenWrt SNAPSHOT r29000" {
		t.Fatalf("expected OpenWrt SNAPSHOT r29000, got %q", got)
	}
}
