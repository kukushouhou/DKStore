package docker

import "testing"

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
