package meta

import (
	"context"
	"testing"

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/store"
)

func TestSaveListAndCascadeCleanup(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport) VALUES ('h1', 'NAS', 'docker', 'local')`); err != nil {
		t.Fatal(err)
	}

	err = SaveBatch(ctx, st, []Entry{
		{
			TargetKey: "h1/compose/immich",
			HostID:    "h1",
			Meta: platform.LocalMeta{
				Alias:      " 家庭相册 ",
				Tags:       []string{"常用", "影音", "常用", " "},
				AccessMode: "auto",
				Scheme:     "http",
				FixedPort:  "2283",
				Note:       "admin@home.local",
			},
		},
		{
			TargetKey: "h1/compose/immich/svc/database",
			HostID:    "h1",
			Meta: platform.LocalMeta{
				AccessMode: "none",
				Tags:       []string{"数据库"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	all, tags, err := ListAll(ctx, st)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2", len(all))
	}
	proj := all["h1/compose/immich"]
	if proj == nil || proj.Alias != "家庭相册" || proj.FixedPort != "2283" || len(proj.Tags) != 2 {
		t.Fatalf("unexpected project meta: %+v", proj)
	}
	dbMeta := all["h1/compose/immich/svc/database"]
	if dbMeta == nil || dbMeta.AccessMode != "none" {
		t.Fatalf("unexpected child meta: %+v", dbMeta)
	}
	if len(tags) != 3 {
		t.Fatalf("tags = %v, want 3 unique tags", tags)
	}

	// 清空字段应删除对应记录
	if err := SaveBatch(ctx, st, []Entry{{
		TargetKey: "h1/compose/immich/svc/database",
		HostID:    "h1",
		Meta:      platform.LocalMeta{AccessMode: "auto"},
	}}); err != nil {
		t.Fatalf("clear child: %v", err)
	}
	all, _, _ = ListAll(ctx, st)
	if len(all) != 1 {
		t.Fatalf("after clear len(all) = %d, want 1", len(all))
	}

	// 删除主机触发外键级联清理
	if _, err := st.DB.ExecContext(ctx, `DELETE FROM host WHERE id='h1'`); err != nil {
		t.Fatal(err)
	}
	all, _, _ = ListAll(ctx, st)
	if len(all) != 0 {
		t.Fatalf("after host delete len(all) = %d, want 0", len(all))
	}
}
