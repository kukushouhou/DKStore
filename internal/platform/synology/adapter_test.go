package synology

import (
	"testing"
)

func TestParseSynoWebAPIProjects(t *testing.T) {
	raw := []byte(`[Line 295] Exec WebAPI:  api=SYNO.Docker.Project, version=1, method=list, param={}, runner=SYSTEM_ADMIN
{
   "data" : {
      "1f475e1a-9480-4d09-8fc2-a96af7f0a4ac" : {
         "containerIds" : [ "e0df1931b3d7f2fd9f09f3460d55e1fb49a01b785f3ddbd518e0d8170ecbe5a9" ],
         "id" : "1f475e1a-9480-4d09-8fc2-a96af7f0a4ac",
         "is_package" : false,
         "name" : "card-bill-assistant",
         "path" : "/volume1/docker/card-bill-assistant",
         "share_path" : "/docker/card-bill-assistant",
         "status" : "RUNNING"
      },
      "ec47b6ca-bc2e-42d1-b9db-09c332166025" : {
         "id" : "ec47b6ca-bc2e-42d1-b9db-09c332166025",
         "is_package" : true,
         "name" : "baiduapp",
         "path" : "/var/packages/baiduapp/target/app",
         "status" : "RUNNING"
      }
   },
   "success" : true
}`)
	projs, err := parseSynoWebAPIProjects(raw)
	if err != nil {
		t.Fatalf("parseSynoWebAPIProjects failed: %v", err)
	}
	if len(projs) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projs))
	}
}
