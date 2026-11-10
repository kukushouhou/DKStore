package ignore

import (
	"context"
	"testing"

	"github.com/kukushouhou/DKStore/internal/store"
)

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

	// Prefix 锁定规则测试（附录 D.3）
	rPref131 := Rule{ServiceKey: "s1", Mode: ModePrefix, Prefix: "131"}
	if Matches(rPref131, "version_update", "sha256:a", "131.0.6778.108") {
		t.Fatal("131.0.6778.108 is inside locked prefix 131, should NOT be filtered (allow notify)")
	}
	if !Matches(rPref131, "version_update", "sha256:b", "132.0.6834.57") {
		t.Fatal("132.0.6834.57 is outside locked prefix 131, MUST be filtered")
	}

	rPref83 := Rule{ServiceKey: "s1", Mode: ModePrefix, Prefix: "8.3"}
	if Matches(rPref83, "version_update", "sha256:c", "v8.3.9-alpine") {
		t.Fatal("v8.3.9-alpine is inside locked prefix 8.3, should NOT be filtered")
	}
	if !Matches(rPref83, "version_update", "sha256:d", "8.9.1") {
		t.Fatal("8.9.1 is outside locked prefix 8.3, MUST be filtered")
	}
	if Matches(rPref83, "digest_update", "sha256:e", "8.3 (d4e5f6)") {
		t.Fatal("digest_update on locked tag 8.3 itself should be allowed")
	}
}

func TestSetWithOptionsAndListAll(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	svc := New(st)
	if err := svc.SetWithOptions(ctx, SetOptions{
		ServiceKeys: []string{"h1/compose/app/web"},
		Mode:        ModePrefix,
		Prefix:      "v131.0",
	}); err != nil {
		t.Fatalf("SetWithOptions prefix: %v", err)
	}
	all, err := svc.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := all["h1/compose/app/web"]
	if got.Mode != ModePrefix || got.Prefix != "131.0" {
		t.Fatalf("unexpected stored prefix rule: %+v", got)
	}
}
