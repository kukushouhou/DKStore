package updater

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
)

func TestResolveAppTargetAndPurgeDeletedState(t *testing.T) {
	up, st := newTestUpdater(t, nil)
	ctx := context.Background()

	// 1. 尚未入库的实时容器（来自 Containers 页面）也能按 <hostId>/<kind>/<name> 正常解析
	hID, name, src, proj, err := up.resolveAppTarget(ctx, "h1/compose/immich", i18n.ZhCN)
	if err != nil {
		t.Fatalf("resolveAppTarget unindexed compose failed: %v", err)
	}
	if hID != "h1" || name != "immich" || src != platform.SourceCompose || proj == nil || proj.Name != "immich" {
		t.Fatalf("unexpected resolved target: %s %s %s %+v", hID, name, src, proj)
	}

	// 2. 已入库的套件托管应用必须拒绝删除
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host(id, name, kind, transport, status, created_at) VALUES ('h1', 'Host 1', 'docker', 'local', 'online', 100)`)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h1/compose/pkg', 'h1', 'pkg', 'vendor_managed', 100)`)
	if _, _, _, _, err := up.resolveAppTarget(ctx, "h1/compose/pkg", i18n.ZhCN); err == nil {
		t.Fatal("expected vendor_managed app to be rejected")
	}

	// 3. 删除完成后立即清理 app / service / finding / ignore_rule / host_runtime_snapshot
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO app(key, host_id, name, source, updated_at) VALUES ('h1/container/portainer', 'h1', 'portainer', 'standalone', 100)`)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO service(key, app_key, name, image_ref, updated_at) VALUES ('h1/container/portainer/main', 'h1/container/portainer', 'portainer', 'portainer/portainer-ce:sts', 100)`)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO finding(service_key, kind, category, checked_at) VALUES ('h1/container/portainer/main', 'version_update', 'normal', 100)`)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO ignore_rule(service_key, mode, created_at) VALUES ('h1/container/portainer/main', 'forever', 100)`)

	snapTrees := []platform.AppRuntimeTree{
		{Key: "h1/container/portainer", HostID: "h1", Name: "portainer", Source: platform.SourceStandalone},
		{Key: "h1/compose/keepme", HostID: "h1", Name: "keepme", Source: platform.SourceCompose},
	}
	snapBytes, _ := json.Marshal(snapTrees)
	_, _ = st.DB.ExecContext(ctx, `INSERT INTO host_runtime_snapshot(host_id, trees_json, updated_at) VALUES ('h1', ?, 100)`, string(snapBytes))

	up.purgeDeletedAppState(ctx, "h1", "h1/container/portainer")

	var goneAt int64
	if err := st.DB.QueryRowContext(ctx, `SELECT COALESCE(gone_at,0) FROM app WHERE key='h1/container/portainer'`).Scan(&goneAt); err != nil || goneAt == 0 {
		t.Fatalf("expected app gone_at > 0, got %d (err=%v)", goneAt, err)
	}
	var findingCnt, ignoreCnt int
	_ = st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM finding WHERE service_key='h1/container/portainer/main'`).Scan(&findingCnt)
	_ = st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ignore_rule WHERE service_key='h1/container/portainer/main'`).Scan(&ignoreCnt)
	if findingCnt != 0 || ignoreCnt != 0 {
		t.Fatalf("expected finding & ignore_rule to be cleaned, got finding=%d ignore=%d", findingCnt, ignoreCnt)
	}

	var updatedSnap string
	_ = st.DB.QueryRowContext(ctx, `SELECT trees_json FROM host_runtime_snapshot WHERE host_id='h1'`).Scan(&updatedSnap)
	var afterTrees []platform.AppRuntimeTree
	_ = json.Unmarshal([]byte(updatedSnap), &afterTrees)
	if len(afterTrees) != 1 || afterTrees[0].Key != "h1/compose/keepme" {
		t.Fatalf("expected deleted app to be removed from host_runtime_snapshot, got %+v", afterTrees)
	}
}
