package platform

import (
	"encoding/json"
	"testing"
)

func TestComposeStreamParser_LayerProgressAndRawANSIPreservation(t *testing.T) {
	parser := NewComposeStreamParser()

	// 1. ANSI 彩色与 \r 进度覆盖流：LogChunk 必须原样保留 ANSI/CR，解析器剥离 ANSI 提取镜像层进度
	chunk1 := "\x1b[1A\x1b[2K  6f837d52a1b9 Pulling fs layer\r\n  a1b2c3d4e5f6 Already exists\r\n"
	p1 := parser.Feed(chunk1)
	if p1.LogChunk != chunk1 {
		t.Fatalf("LogChunk must preserve raw ANSI/CR bytes, got %q", p1.LogChunk)
	}
	if p1.TotalLayers != 2 || p1.CompletedLayers != 1 {
		t.Fatalf("expected 1/2 layers completed, got %+v", p1)
	}
	if len(p1.Layers) != 2 || p1.Layers[0].ID != "6f837d52a1b9" || p1.Layers[0].Status != "waiting" || p1.Layers[1].Status != "done" {
		t.Fatalf("unexpected layers slice: %+v", p1.Layers)
	}
	// 待下载层仍处于 waiting（0 字节），整体百分比必须保持在真实 0%，禁止硬编码假起步
	if p1.Percent != 0 {
		t.Fatalf("expected 0%% while uncached layer is still waiting, got %d", p1.Percent)
	}

	// 2. 分块截断 + \r 覆盖行解析下载字节与百分比
	partA := "\r\x1b[2K  6f837d52a1b9 Downloading [=========>          ]  15.00MB/"
	parser.Feed(partA)
	partB := "30.00MB\r"
	p2 := parser.Feed(partB)
	if p2.Layers[0].Status != "downloading" || p2.Layers[0].Percent != 50 {
		t.Fatalf("expected downloading 50%%, got %+v", p2.Layers[0])
	}
	if p2.DownloadedBytes != 15_000_000 || p2.TotalBytes != 30_000_000 {
		t.Fatalf("unexpected bytes %d / %d", p2.DownloadedBytes, p2.TotalBytes)
	}
	if p2.Percent <= 0 || p2.Percent >= 85 {
		t.Fatalf("expected downloading percent between 1 and 84, got %d", p2.Percent)
	}

	// 3. 解压 → 完成 → 容器重建
	p3 := parser.Feed("  \x1b[32m✔\x1b[0m 6f837d52a1b9 Pull complete\r\n")
	if p3.CompletedLayers != 2 || p3.Percent != 85 {
		t.Fatalf("expected 2/2 done at 85%%, got completed=%d pct=%d", p3.CompletedLayers, p3.Percent)
	}

	p4 := parser.Feed(" \x1b[36mContainer card-bill-assistant-1  Recreating\x1b[0m\r\n")
	if p4.Phase != "recreating" || p4.Percent != 90 {
		t.Fatalf("expected recreating at 90%%, got phase=%s pct=%d", p4.Phase, p4.Percent)
	}
}

func TestComputeWeightedPullProgress_ManyWaitingLayersNeverJumpEarly(t *testing.T) {
	// 复现 21 层镜像：前 3 个小层完成，后 18 个大层仍在 waiting
	// 1) 预读到 Manifest 全量层体积时：2.4MB / 612.4MB -> 仅 ~0%..1%
	withSizes := make([]WeightedLayerInput, 21)
	for i := 0; i < 21; i++ {
		if i < 3 {
			withSizes[i] = WeightedLayerInput{Status: "done", DLCurrent: 800_000, DLTotal: 800_000, StagePct: 100}
		} else {
			withSizes[i] = WeightedLayerInput{Status: "waiting", DLTotal: 33_888_888, StagePct: 0}
		}
	}
	dl, tot, pct := ComputeWeightedPullProgress(withSizes)
	if dl != 2_400_000 || tot < 600_000_000 {
		t.Fatalf("expected full manifest total bytes, got %d / %d", dl, tot)
	}
	if pct > 5 {
		t.Fatalf("expected tiny percent (<5%%) when only 2.4MB/612MB done, got %d%%", pct)
	}

	// 2) 未能预读到等待层体积（DLTotal=0）兜底：也不得将 18 个等待层按 0B 漏算冲上 80%
	noSizes := make([]WeightedLayerInput, 21)
	for i := 0; i < 21; i++ {
		if i < 3 {
			noSizes[i] = WeightedLayerInput{Status: "done", DLCurrent: 800_000, DLTotal: 800_000, StagePct: 100}
		} else {
			noSizes[i] = WeightedLayerInput{Status: "waiting", DLTotal: 0, StagePct: 0}
		}
	}
	_, _, fallbackPct := ComputeWeightedPullProgress(noSizes)
	if fallbackPct > 20 {
		t.Fatalf("expected fallback percent <= 20%% with 18/21 layers waiting, got %d%%", fallbackPct)
	}
}

func TestComposeStreamParser_FatalDaemonErrorDetection(t *testing.T) {
	parser := NewComposeStreamParser()
	parser.Feed("Pulling app ...\r\nError response from daemon: manifest for ghcr.io/org/app:v9.9.9 not found: manifest unknown\r\n")
	if parser.FatalError() == "" {
		t.Fatal("expected FatalError to detect daemon manifest unknown error")
	}
}

func TestPortBinding_UnmarshalFlexNumberAndString(t *testing.T) {
	raw := `[{"hostIp":"0.0.0.0","hostPort":8080,"containerPort":"80","protocol":""}]`
	var ports []PortBinding
	if err := json.Unmarshal([]byte(raw), &ports); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(ports) != 1 || ports[0].HostPort != "8080" || ports[0].ContainerPort != "80" || ports[0].Protocol != "tcp" {
		t.Fatalf("unexpected port binding: %+v", ports)
	}
}
