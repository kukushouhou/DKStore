package docker

import (
	"strings"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
)

func TestUpdateDaemonJSONMirrorsPreservesOtherFields(t *testing.T) {
	orig := []byte(`{"data-root":"/data/docker","registry-mirrors":["https://docker.mirrors.ustc.edu.cn/"],"storage-driver":"overlay2"}`)
	urls, ok := ExtractMirrorsFromDaemonJSON(orig)
	if !ok || len(urls) != 1 || urls[0] != "https://docker.mirrors.ustc.edu.cn" {
		t.Fatalf("unexpected extracted mirrors: ok=%v urls=%v", ok, urls)
	}

	updated, _, err := UpdateDaemonJSONMirrors(orig, []string{"docker.1ms.run/", "https://docker.m.daocloud.io"})
	if err != nil {
		t.Fatalf("UpdateDaemonJSONMirrors failed: %v", err)
	}
	s := string(updated)
	if !strings.Contains(s, `"/data/docker"`) || !strings.Contains(s, `"overlay2"`) {
		t.Fatalf("expected existing fields preserved, got: %s", s)
	}
	nextURLs, ok2 := ExtractMirrorsFromDaemonJSON(updated)
	if !ok2 || len(nextURLs) != 2 || nextURLs[0] != "https://docker.1ms.run" || nextURLs[1] != "https://docker.m.daocloud.io" {
		t.Fatalf("unexpected updated mirrors: %v", nextURLs)
	}

	cleared, _, err := UpdateDaemonJSONMirrors(updated, nil)
	if err != nil {
		t.Fatalf("clear mirrors failed: %v", err)
	}
	if strings.Contains(string(cleared), "registry-mirrors") {
		t.Fatalf("expected registry-mirrors key deleted when empty, got: %s", string(cleared))
	}
}

func TestNormalizeMirrorPairsAndBuildMirrorRef(t *testing.T) {
	pairs := NormalizeMirrorPairs([]platform.MirrorPair{
		{Source: "https://index.docker.io/", URLs: []string{"docker.1ms.run/", "https://docker.1ms.run"}},
		{Source: "https://ghcr.io/", URLs: []string{"ghcr.nju.edu.cn"}},
	})
	if len(pairs) != 2 || pairs[0].Source != "docker.io" || len(pairs[0].URLs) != 1 || pairs[1].Source != "ghcr.io" {
		t.Fatalf("unexpected normalized pairs: %+v", pairs)
	}

	reg, repo, tag := splitImageRegistryAndRepoPath("ghcr.io/owner/app:v1.2.3")
	if reg != "ghcr.io" || repo != "owner/app" || tag != "v1.2.3" {
		t.Fatalf("unexpected split: %s / %s / %s", reg, repo, tag)
	}
	ref := buildMirrorPullRef("https://ghcr.nju.edu.cn/", repo, tag)
	if ref != "ghcr.nju.edu.cn/owner/app:v1.2.3" {
		t.Fatalf("unexpected mirror pull ref: %s", ref)
	}
}
