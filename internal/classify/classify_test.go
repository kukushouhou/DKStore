package classify

import (
	"testing"

	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/version"
)

func TestEvaluateCases(t *testing.T) {
	// 1. 普通补丁/次版本更新 -> normal
	ref1, _ := version.ParseImageRef("nginx:1.27.1")
	out1 := Evaluate(Input{
		Ref:             ref1,
		LocalRepoDigest: "sha256:aaa",
		RemoteManifest:  &registry.ManifestResult{RepoDigest: "sha256:aaa"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "1.27.1", RepoDigest: "sha256:aaa"},
			{Name: "1.27.2", RepoDigest: "sha256:bbb"},
		},
	})
	if out1.Kind != KindVersionUpdate || out1.Category != CategoryNormal || out1.TargetVersion != "1.27.2" {
		t.Fatalf("case 1 unexpected: %+v", out1)
	}

	// 2. 跨大版本 2.9.0 -> 3.0.0 -> caution (major_bump)
	ref2, _ := version.ParseImageRef("traefik:2.9.0")
	out2 := Evaluate(Input{
		Ref:             ref2,
		LocalRepoDigest: "sha256:aaa",
		RemoteManifest:  &registry.ManifestResult{RepoDigest: "sha256:aaa"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "2.9.0", RepoDigest: "sha256:aaa"},
			{Name: "3.0.0", RepoDigest: "sha256:ccc"},
		},
	})
	if out2.Kind != KindVersionUpdate || out2.Category != CategoryCaution || out2.Reason != "major_bump" {
		t.Fatalf("case 2 unexpected: %+v", out2)
	}

	// 3. 数据库 mysql:8.0.40 存在 8.0.41 与 8.4.2 -> 只提醒 8.0.41 且在 caution，Hint 提示 8.4.2
	ref3, _ := version.ParseImageRef("mysql:8.0.40")
	out3 := Evaluate(Input{
		Ref:             ref3,
		LocalRepoDigest: "sha256:aaa",
		RemoteManifest:  &registry.ManifestResult{RepoDigest: "sha256:aaa"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "8.0.40", RepoDigest: "sha256:aaa"},
			{Name: "8.0.41", RepoDigest: "sha256:bbb"},
			{Name: "8.4.2", RepoDigest: "sha256:ccc"},
		},
	})
	if out3.Kind != KindVersionUpdate || out3.Category != CategoryCaution || out3.TargetVersion != "8.0.41" || out3.Hint == "" {
		t.Fatalf("case 3 unexpected: %+v", out3)
	}

	// 4. 多架构仅其他平台变化（RepoDigest 变了，但本平台 PlatformDigest 等于 LocalRepoDigest）-> KindNone
	ref4, _ := version.ParseImageRef("nginx:latest")
	out4 := Evaluate(Input{
		Ref:             ref4,
		LocalRepoDigest: "sha256:platAmd64",
		RemoteManifest: &registry.ManifestResult{
			RepoDigest:     "sha256:newIndex",
			PlatformDigest: "sha256:platAmd64",
		},
	})
	if out4.Kind != KindNone {
		t.Fatalf("case 4 expected KindNone, got %+v", out4)
	}
}
