package version

import "testing"

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		in   string
		reg  string
		repo string
		tag  string
		dig  string
	}{
		{"nginx", "docker.io", "library/nginx", "latest", ""},
		{"nginx:1.27.1-alpine", "docker.io", "library/nginx", "1.27.1-alpine", ""},
		{"index.docker.io/library/postgres:16.4", "docker.io", "library/postgres", "16.4", ""},
		{"linuxserver/jellyfin:latest", "docker.io", "linuxserver/jellyfin", "latest", ""},
		{"lscr.io/linuxserver/jellyfin:10.9.11", "lscr.io", "linuxserver/jellyfin", "10.9.11", ""},
		{"ghcr.io/home-assistant/home-assistant:2024.10.1", "ghcr.io", "home-assistant/home-assistant", "2024.10.1", ""},
		{"10.0.0.2:5000/my/app:v1.2.0", "10.0.0.2:5000", "my/app", "v1.2.0", ""},
		{"nginx@sha256:abcdef", "docker.io", "library/nginx", "", "sha256:abcdef"},
		{"nginx:1.27@sha256:abcdef", "docker.io", "library/nginx", "1.27", "sha256:abcdef"},
	}
	for _, c := range cases {
		got, err := ParseImageRef(c.in)
		if err != nil {
			t.Fatalf("ParseImageRef(%q) error: %v", c.in, err)
		}
		if got.Registry != c.reg || got.Repository != c.repo || got.Tag != c.tag || got.Digest != c.dig {
			t.Errorf("ParseImageRef(%q) = %+v, want reg=%s repo=%s tag=%s dig=%s",
				c.in, got, c.reg, c.repo, c.tag, c.dig)
		}
	}
}

func TestParseTagAndSelect(t *testing.T) {
	p := ParseTag("1.27.1-alpine")
	if p.Scheme != SchemeSemVer || p.Major != 1 || p.Minor != 27 || p.Patch != 1 || p.Variant != "alpine" || p.Precision != 3 {
		t.Fatalf("unexpected parse: %+v", p)
	}

	cal := ParseTag("2024.10.1")
	if cal.Scheme != SchemeCalVer || cal.Major != 2024 || cal.Minor != 10 || cal.Patch != 1 {
		t.Fatalf("expected calver: %+v", cal)
	}

	ls := ParseTag("10.9.11-ls42")
	if ls.Scheme != SchemeSemVer || ls.Build != 42 || ls.Variant != "ls" {
		t.Fatalf("expected linuxserver tag: %+v", ls)
	}

	rc := ParseTag("1.28.0-rc1")
	if !rc.Prerelease {
		t.Fatalf("expected prerelease: %+v", rc)
	}

	best := SelectBestDisplayTag("latest", []string{"latest", "1", "1.8", "1.8.5", "1.8.5-alpine"})
	if best != "1.8.5" {
		t.Fatalf("SelectBestDisplayTag = %q, want 1.8.5", best)
	}
}

func TestNormalizeRegistry(t *testing.T) {
	cases := map[string]string{
		"https://ghcr.io/":                     "ghcr.io",
		"http://192.168.31.8:5000/v2/":         "192.168.31.8:5000",
		"  INDEX.DOCKER.IO ":                   "docker.io",
		"registry.cn-hangzhou.aliyuncs.com":    "registry.cn-hangzhou.aliyuncs.com",
		"https://registry-1.docker.io/v2/repo": "docker.io",
	}
	for in, want := range cases {
		if got := NormalizeRegistry(in); got != want {
			t.Errorf("NormalizeRegistry(%q) = %q, want %q", in, got, want)
		}
	}
}
