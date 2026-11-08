package ignore

import "testing"

func TestMatches(t *testing.T) {
	rForever := Rule{ServiceKey: "s1", Mode: ModeForever}
	if !Matches(rForever, "digest_update", "sha256:new", "1.2") {
		t.Fatal("forever should always match")
	}

	rDigest := Rule{ServiceKey: "s1", Mode: ModeThisVersion, TargetDigest: "sha256:d1"}
	if !Matches(rDigest, "digest_update", "sha256:d1", "检测到新镜像") {
		t.Fatal("same digest should match")
	}
	if Matches(rDigest, "digest_update", "sha256:d2", "检测到新镜像") {
		t.Fatal("new digest should wake up reminder")
	}

	rTag := Rule{ServiceKey: "s1", Mode: ModeThisVersion, TargetTag: "1.27.2"}
	if !Matches(rTag, "version_update", "sha256:x", "1.27.2") {
		t.Fatal("same version tag should match")
	}
	if Matches(rTag, "version_update", "sha256:y", "1.27.3") {
		t.Fatal("higher version tag should wake up reminder")
	}
}
