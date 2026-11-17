package rules

import "testing"

func TestResolveContainerLinks_PreservesAllAndDerivesReleases(t *testing.T) {
	rs := Default()

	// 1. 容器标签同时提供 source (GitHub)、url (官方网站)、documentation (官方文档)
	got := rs.ResolveContainerLinks("docker.io", "myorg/myapp", map[string]string{
		"org.opencontainers.image.source":        "https://github.com/myorg/myapp.git",
		"org.opencontainers.image.url":           "https://myapp.example.com",
		"org.opencontainers.image.documentation": "https://docs.myapp.example.com",
	}, "")
	if got.Repo != "https://github.com/myorg/myapp" {
		t.Fatalf("Repo = %q, want https://github.com/myorg/myapp", got.Repo)
	}
	if got.Releases != "https://github.com/myorg/myapp/releases" {
		t.Fatalf("Releases = %q, want https://github.com/myorg/myapp/releases", got.Releases)
	}
	if got.Website != "https://myapp.example.com" {
		t.Fatalf("Website = %q, want https://myapp.example.com", got.Website)
	}
	if got.Docs != "https://docs.myapp.example.com" {
		t.Fatalf("Docs = %q, want https://docs.myapp.example.com", got.Docs)
	}
	if got.RegistryURL != "https://hub.docker.com/r/myorg/myapp" {
		t.Fatalf("RegistryURL = %q, want https://hub.docker.com/r/myorg/myapp", got.RegistryURL)
	}

	// 2. 非源码仓的官方网站照常保留，仅不强造发行说明
	siteOnly := rs.ResolveContainerLinks("docker.io", "myorg/portal", map[string]string{
		"org.opencontainers.image.url":           "https://portal.example.com",
		"org.opencontainers.image.documentation": "https://portal.example.com/guide",
	}, "")
	if siteOnly.Website != "https://portal.example.com" {
		t.Fatalf("Website = %q, want https://portal.example.com", siteOnly.Website)
	}
	if siteOnly.Docs != "https://portal.example.com/guide" {
		t.Fatalf("Docs = %q, want https://portal.example.com/guide", siteOnly.Docs)
	}
	if siteOnly.Releases != "" {
		t.Fatalf("Releases = %q, want empty", siteOnly.Releases)
	}

	// 3. 过滤基础镜像（如 FROM python / alpine）遗留给业务镜像的标签
	polluted := rs.ResolveContainerLinks("docker.io", "arc-mx/sgcc_electricity", map[string]string{
		"org.opencontainers.image.source": "https://github.com/alpinelinux/docker-alpine",
		"org.opencontainers.image.url":    "https://www.python.org",
	}, "")
	if polluted.Website != "" || polluted.Releases != "" {
		t.Fatalf("expected inherited base labels filtered, got %+v", polluted)
	}
	if polluted.Repo != "https://hub.docker.com/r/arc-mx/sgcc_electricity" {
		t.Fatalf("Repo = %q, want Docker Hub fallback", polluted.Repo)
	}
}
