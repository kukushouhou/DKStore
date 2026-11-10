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
