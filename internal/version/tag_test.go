package version

import "testing"

func TestMultiSegmentVersionsAndMatchesPrefix(t *testing.T) {
	p1 := ParseTag("2")
	if p1.Scheme != SchemeSemVer || p1.Precision != 1 || len(p1.Segments) != 1 || p1.Major != 2 || p1.CoreVersion() != "2" {
		t.Fatalf("unexpected 1-part parse: %+v", p1)
	}

	p2 := ParseTag("16.4")
	if p2.Scheme != SchemeSemVer || p2.Precision != 2 || p2.CoreVersion() != "16.4" {
		t.Fatalf("unexpected 2-part parse: %+v", p2)
	}

	p4a := ParseTag("131.0.6778.85")
	p4b := ParseTag("131.0.6778.108")
	if p4a.Precision != 4 || p4a.CoreVersion() != "131.0.6778.85" {
		t.Fatalf("unexpected 4-part parse: %+v", p4a)
	}
	if Compare(p4a, p4b) >= 0 {
		t.Fatalf("expected 131.0.6778.85 < 131.0.6778.108")
	}
	if !CompatibleCandidate(p4a, p4b) {
		t.Fatalf("expected 4-part tags to be compatible candidates")
	}
	if !SameSeries(p4a, p4b, "major") || !SameSeries(p4a, p4b, "minor") {
		t.Fatalf("expected same series for 131.0.x.y")
	}

	p5 := ParseTag("v1.2.3.4.5")
	if p5.Precision != 5 || !p5.HasVPrefix || p5.CoreVersion() != "1.2.3.4.5" {
		t.Fatalf("unexpected 5-part parse: %+v", p5)
	}

	ls4 := ParseTag("v131.0.6778.85-ls123")
	ls4Higher := ParseTag("v131.0.6778.85-ls124")
	if ls4.Precision != 4 || ls4.Build != 123 || ls4.Variant != "ls" {
		t.Fatalf("unexpected 4-part ls parse: %+v", ls4)
	}
	if Compare(ls4, ls4Higher) >= 0 {
		t.Fatalf("expected ls123 < ls124")
	}

	for _, pref := range []string{"131", "v131", "131.0", "131.0.6778", "131.0.6778.108"} {
		if !MatchesPrefix(p4b, pref) {
			t.Fatalf("expected %s to match prefix %q", p4b.Raw, pref)
		}
	}
	for _, nonMatch := range []string{"13", "132", "131.1", "131.0.6779", "131.0.6778.108.1", ""} {
		if MatchesPrefix(p4b, nonMatch) {
			t.Fatalf("expected %s NOT to match prefix %q", p4b.Raw, nonMatch)
		}
	}
}

func TestSelectBestDisplayTag_PreservesNonSemVerAliases(t *testing.T) {
	gotDate := SelectBestDisplayTag("latest", []string{"latest", "edge", "20261110-1", "20261110-3"})
	if gotDate != "20261110-3" {
		t.Fatalf("expected date alias 20261110-3, got %q", gotDate)
	}

	gotSha := SelectBestDisplayTag("latest", []string{"latest", "main", "sha-8a3f1c2"})
	if gotSha != "sha-8a3f1c2" {
		t.Fatalf("expected sha alias sha-8a3f1c2, got %q", gotSha)
	}

	gotBuild := SelectBestDisplayTag("stable", []string{"stable", "build-512"})
	if gotBuild != "build-512" {
		t.Fatalf("expected build-512, got %q", gotBuild)
	}

	gotOnlyRolling := SelectBestDisplayTag("latest", []string{"latest", "main"})
	if gotOnlyRolling != "" {
		t.Fatalf("expected empty when only rolling tags exist, got %q", gotOnlyRolling)
	}
}

func TestPrereleaseParsingCompareAndCompatibility(t *testing.T) {
	b1 := ParseTag("3.0.0-beta.1")
	b2 := ParseTag("3.0.0-beta.2")
	rc1 := ParseTag("3.0.0-rc.1")
	final3 := ParseTag("3.0.0")
	b1Slim := ParseTag("3.0.0-beta.1-slim")
	stable2 := ParseTag("2.5.11")

	if !b1.Prerelease || b1.Variant != "" || b1.PreStage != 3 || len(b1.PreNums) != 1 || b1.PreNums[0] != 1 {
		t.Fatalf("unexpected b1 parse: %+v", b1)
	}
	if !b1Slim.Prerelease || b1Slim.Variant != "slim" {
		t.Fatalf("expected b1Slim variant=slim, got %+v", b1Slim)
	}
	if !(Compare(b1, b2) < 0 && Compare(b2, rc1) < 0 && Compare(rc1, final3) < 0) {
		t.Fatalf("expected 3.0.0-beta.1 < 3.0.0-beta.2 < 3.0.0-rc.1 < 3.0.0")
	}
	if CompatibleCandidate(stable2, b1) {
		t.Fatalf("stable 2.5.11 must not auto-recommend prerelease 3.0.0-beta.1")
	}
	if !CompatibleCandidate(b1, b2) || !CompatibleCandidate(b1, final3) {
		t.Fatalf("prerelease 3.0.0-beta.1 must accept 3.0.0-beta.2 and 3.0.0 as upgrade candidates")
	}
	if CompatibleCandidate(b1, b1Slim) {
		t.Fatalf("plain 3.0.0-beta.1 must not match -slim variant")
	}
	if !IsPrereleaseAny("beta") || !IsPrereleaseAny("3.0.0-beta.1") || IsPrereleaseAny("2.5.12") || IsPrereleaseAny("latest") {
		t.Fatalf("unexpected IsPrereleaseAny results")
	}
	if got := SelectBestDisplayTag("beta", []string{"beta", "3.0.0-beta.1"}); got != "3.0.0-beta.1" {
		t.Fatalf("expected beta -> 3.0.0-beta.1, got %q", got)
	}
	if got := SelectBestDisplayTag("beta-slim", []string{"beta-slim", "3.0.0-beta.1", "3.0.0-beta.1-slim"}); got != "3.0.0-beta.1-slim" {
		t.Fatalf("expected beta-slim -> 3.0.0-beta.1-slim, got %q", got)
	}
}

func TestSelectUpdateCandidatesAndFlavorVersionPinning(t *testing.T) {
	pool := []string{
		"v1.118.1",
		"v1.118.4",
		"v1.119.0",
		"v1.124.2",
		"v2.0.0-rc.1",
		"v2.0.0",
		"v2.1.0",
		"v1.118.9-alpine",
	}
	cands := SelectUpdateCandidates("v1.118.0", pool)
	if cands.Patch != "v1.118.4" || cands.Minor != "v1.124.2" || cands.Major != "v2.1.0" || cands.Count() != 3 {
		t.Fatalf("unexpected candidates for v1.118.0: %+v", cands)
	}

	// LinuxServer -ls 构建号补丁识别
	lsCands := SelectUpdateCandidates("1.2.3-ls10", []string{"1.2.3-ls12", "1.2.4-ls11", "1.3.0-ls5"})
	if lsCands.Patch != "1.2.4-ls11" || lsCands.Minor != "1.3.0-ls5" || lsCands.Major != "" {
		t.Fatalf("unexpected ls candidates: %+v", lsCands)
	}

	// 0.x.y 早期版本号：0.5.1 -> 补丁 0.5.4、次版本 0.6.2、主版本 1.0.0 互不覆盖
	zeroCands := SelectUpdateCandidates("0.5.1", []string{"0.5.4", "0.6.2", "1.0.0"})
	if zeroCands.Patch != "0.5.4" || zeroCands.Minor != "0.6.2" || zeroCands.Major != "1.0.0" {
		t.Fatalf("unexpected 0.x candidates: %+v", zeroCands)
	}

	// 发行版后缀锁死版本识别与双向防串台（如 1.27-alpine vs 1.27-alpine3.20 / debian12 / ubuntu22.04）
	for _, pinned := range []string{"alpine3.20", "alpine-3.20", "alpine.3.20", "ubuntu22.04", "ubuntu.22.04", "debian12", "slim-openssl3.1"} {
		if !HasFlavorVersionPin(pinned) {
			t.Fatalf("expected HasFlavorVersionPin(%q) == true", pinned)
		}
	}
	for _, floating := range []string{"", "alpine", "slim", "bookworm", "ls", "amd64", "arm64v8"} {
		if HasFlavorVersionPin(floating) {
			t.Fatalf("expected HasFlavorVersionPin(%q) == false", floating)
		}
	}

	pairs := [][2]string{
		{"1.27-alpine", "1.27-alpine3.20"},
		{"1.27-alpine3.20", "1.27-alpine"},
		{"1.27-alpine", "1.27-alpine.3.20"},
		{"16.4-debian", "16.5-debian12"},
		{"2.8-rc1-alpine", "2.8-alpine3.20"},
		{"2.8-rc1-alpine3.20", "2.8-alpine"},
	}
	for _, p := range pairs {
		cur := ParseTag(p[0])
		cand := ParseTag(p[1])
		if CompatibleCandidate(cur, cand) {
			t.Fatalf("expected %q and %q to be incompatible across floating/pinned flavor boundary", p[0], p[1])
		}
	}
	if !CompatibleCandidate(ParseTag("2.8-rc1-alpine"), ParseTag("2.8-alpine")) {
		t.Fatalf("expected 2.8-rc1-alpine -> 2.8-alpine to be compatible")
	}
	if !CompatibleCandidate(ParseTag("2.8-rc1-alpine3.20"), ParseTag("2.8-alpine3.20")) {
		t.Fatalf("expected 2.8-rc1-alpine3.20 -> 2.8-alpine3.20 to be compatible")
	}
}
