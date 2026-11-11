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

func TestAppendixD_PrecisionScopingAndDigestFormattingAndStatefulRules(t *testing.T) {
	// 1. 单段标签 :2 绝不越界推荐 :3 / :9
	ref1, _ := version.ParseImageRef("eclipse-mosquitto:2")
	out1 := Evaluate(Input{
		Ref:                ref1,
		LocalRepoDigest:    "sha256:aaaa111122223333",
		RemoteManifest:     &registry.ManifestResult{RepoDigest: "sha256:aaaa111122223333"},
		CurrentMatchedTags: []string{"2", "2.0.20"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "2", RepoDigest: "sha256:aaaa111122223333"},
			{Name: "3", RepoDigest: "sha256:bbbb444455556666"},
			{Name: "9", RepoDigest: "sha256:cccc777788889999"},
		},
	})
	if out1.Kind != KindNone || out1.RoleKind != "mq" || out1.CurrentVersion != "2 (2.0.20)" {
		t.Fatalf("1-part precision scoping failed: %+v", out1)
	}

	// 2. 两段标签 :8.3 绝不越界推荐 :8.4 / :8.9
	ref2, _ := version.ParseImageRef("php:8.3")
	out2 := Evaluate(Input{
		Ref:             ref2,
		LocalRepoDigest: "sha256:1111222233334444",
		RemoteManifest:  &registry.ManifestResult{RepoDigest: "sha256:1111222233334444"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "8.3", RepoDigest: "sha256:1111222233334444"},
			{Name: "8.4", RepoDigest: "sha256:5555666677778888"},
			{Name: "8.9", RepoDigest: "sha256:9999aaaabbbbcccc"},
		},
	})
	if out2.Kind != KindNone || out2.CurrentVersion != "8.3" {
		t.Fatalf("2-part precision scoping failed: %+v", out2)
	}

	// 3. 消除 "2 -> 2"：当 :2 摘要变化但未反查到更细标签时，输出 "2 (a1b2c3) -> 2 (d4e5f6)"
	out3 := Evaluate(Input{
		Ref:                ref1,
		LocalRepoDigest:    "sha256:a1b2c31111111111",
		RemoteManifest:     &registry.ManifestResult{RepoDigest: "sha256:d4e5f62222222222"},
		CurrentMatchedTags: []string{"2"},
		RemoteMatchedTags:  []string{"2"},
	})
	if out3.Kind != KindDigestUpdate || out3.CurrentVersion != "2 (a1b2c3)" || out3.TargetVersion != "2 (d4e5f6)" {
		t.Fatalf("expected short hash diff instead of 2 -> 2, got %+v", out3)
	}

	// 4. 消除 "当前版本未知 / 检测到新镜像"：:latest 无任何别名标签时输出 "latest (112233) -> latest (445566)"
	refLatest, _ := version.ParseImageRef("guovern/iptv-api:latest")
	out4 := Evaluate(Input{
		Ref:                refLatest,
		LocalRepoDigest:    "sha256:112233aaaaaaaaaa",
		RemoteManifest:     &registry.ManifestResult{RepoDigest: "sha256:445566bbbbbbbbbb"},
		CurrentMatchedTags: []string{"latest"},
		RemoteMatchedTags:  []string{"latest"},
	})
	if out4.Kind != KindDigestUpdate || out4.CurrentVersion != "latest (112233)" || out4.TargetVersion != "latest (445566)" {
		t.Fatalf("expected latest short hash diff, got %+v", out4)
	}

	// 5. 四段版本号更新（Chrome 131.0.6778.85 -> 131.0.6778.108 normal；-> 132.0.6834.57 caution）
	refChrome, _ := version.ParseImageRef("linuxserver/chromium:131.0.6778.85")
	out5 := Evaluate(Input{
		Ref:             refChrome,
		LocalRepoDigest: "sha256:aaa111",
		RemoteManifest:  &registry.ManifestResult{RepoDigest: "sha256:aaa111"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "131.0.6778.108", RepoDigest: "sha256:bbb222"},
			{Name: "132.0.6834.57", RepoDigest: "sha256:ccc333"},
		},
	})
	if out5.Kind != KindVersionUpdate || out5.Category != CategoryCaution || out5.TargetVersion != "132.0.6834.57" {
		t.Fatalf("4-part major bump classification failed: %+v", out5)
	}

	// 6. 非白名单自定义镜像按服务名 "broker" 兜底识别为需谨慎更新（mq）
	refCustomMQ, _ := version.ParseImageRef("mycorp/internal-bus:1.2.0")
	out6 := Evaluate(Input{
		Ref:             refCustomMQ,
		ServiceName:     "broker",
		LocalRepoDigest: "sha256:111",
		RemoteManifest:  &registry.ManifestResult{RepoDigest: "sha256:111"},
		CandidateTags: []registry.TagDigestEntry{
			{Name: "1.2.1", RepoDigest: "sha256:222"},
		},
	})
	if out6.Kind != KindVersionUpdate || out6.Category != CategoryCaution || out6.Reason != "database_suspected" || out6.RoleKind != "mq" {
		t.Fatalf("expected heuristic mq caution, got %+v", out6)
	}

	// 7. 专门的浮动更新标签（如 2.6 / latest）提取到具体版本号时必须展示 "2.6 (2.6.8)" / "latest (v3.16.0)"
	refSeries, _ := version.ParseImageRef("redis:2.6")
	out7 := Evaluate(Input{
		Ref:                refSeries,
		LocalRepoDigest:    "sha256:old264aaaaaaaaaa",
		RemoteManifest:     &registry.ManifestResult{RepoDigest: "sha256:new268bbbbbbbbbb"},
		CurrentMatchedTags: []string{"2.6.4"},
		RemoteMatchedTags:  []string{"2.6", "2.6.8"},
	})
	if out7.Kind != KindDigestUpdate || out7.CurrentVersion != "2.6 (2.6.4)" || out7.TargetVersion != "2.6 (2.6.8)" {
		t.Fatalf("expected 2.6 (2.6.4) -> 2.6 (2.6.8), got %+v", out7)
	}

	refProm, _ := version.ParseImageRef("prom/prometheus:latest")
	out8 := Evaluate(Input{
		Ref:                refProm,
		LocalRepoDigest:    "sha256:old391aaaaaaaaaa",
		RemoteManifest:     &registry.ManifestResult{RepoDigest: "sha256:new3160bbbbbbbbb"},
		CurrentMatchedTags: []string{"v3.9.1"},
		RemoteMatchedTags:  []string{"latest", "v3.16.0"},
	})
	if out8.Kind != KindDigestUpdate || out8.CurrentVersion != "latest (v3.9.1)" || out8.TargetVersion != "latest (v3.16.0)" {
		t.Fatalf("expected latest (v3.9.1) -> latest (v3.16.0), got %+v", out8)
	}
}
