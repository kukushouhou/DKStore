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
