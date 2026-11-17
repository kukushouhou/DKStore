package platform

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	reANSI = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07)`)
	// 匹配 Docker / Docker Compose 输出的 8~16 位十六进制镜像层行（兼容前导勾选符/旋转符或冒号）
	reComposeLayer = regexp.MustCompile(`^(?:[^\w\n\r]*\s+)?([0-9a-f]{8,16})(?::|\s+)\s*(.+)$`)
	// 匹配形如 "18.42MB/31.60MB" 或 "512 kB / 1.2 MB" 的字节进度对
	reBytePair = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*([KMGTPE]?i?B)\s*/\s*(\d+(?:\.\d+)?)\s*([KMGTPE]?i?B)`)
)

// StripANSI 去除字符串中的 ANSI 转义控制序列（仅用于文本行解析，不影响发给 xterm 的原始流）。
func StripANSI(s string) string {
	return reANSI.ReplaceAllString(s, "")
}

// LookupLayerSize 根据 Docker 输出的短层 ID（通常为 8~16 位十六进制前缀）在预读的 Manifest 层体积字典中匹配真实压缩字节数。
func LookupLayerSize(sizes map[string]int64, layerID string) int64 {
	if len(sizes) == 0 {
		return 0
	}
	id := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(layerID, "sha256:")))
	if id == "" {
		return 0
	}
	if sz, ok := sizes[id]; ok && sz > 0 {
		return sz
	}
	if len(id) < 8 {
		return 0
	}
	for k, sz := range sizes {
		if sz <= 0 {
			continue
		}
		if strings.HasPrefix(k, id) || strings.HasPrefix(id, k) {
			return sz
		}
	}
	return 0
}

// WeightedLayerInput 描述单层用于计算整体拉取百分比与真实下载流量的输入状态。
type WeightedLayerInput struct {
	Status    string // waiting | downloading | extracting | done
	Cached    bool   // true 表示本地 Already exists，无需网络下载
	DLCurrent int64  // 已下载压缩字节数
	DLTotal   int64  // 该层真实压缩总字节数（来自 Manifest 预读或 Downloading 事件）
	StagePct  int    // 当前阶段（下载或解压）0..100 进度
}

// ComputeWeightedPullProgress 按全量待拉取镜像层的真实体积加权计算下载字节数与整体百分比（0%..85%）：
//   - 未产生任何实际下载/解压进度前严格返回 0%（消除硬编码 5%/10% 假起步）；
//   - 每层内下载占 70% 权重、解压占 30% 权重；本地已缓存层（Already exists）不计入待下载体积；
//   - 若个别场景未能预读到等待层大小，对未知大小的等待层按已知层平均体积估算权重，绝不按 0 B 漏算导致提前冲顶。
func ComputeWeightedPullProgress(items []WeightedLayerInput) (downloadedBytes, totalBytes int64, pullPercent int) {
	if len(items) == 0 {
		return 0, 0, 0
	}

	var pullItems []WeightedLayerInput
	allDone := true
	for _, it := range items {
		if it.Status != "done" {
			allDone = false
		}
		if !it.Cached {
			pullItems = append(pullItems, it)
		}
	}

	// 若全部层均为本地缓存（Already exists），仅在全部确认完成后进入 85%
	if len(pullItems) == 0 {
		if allDone {
			return 0, 0, 85
		}
		return 0, 0, 0
	}

	anyActive := false
	allPullDone := true
	var knownSizeSum int64
	knownSizeCount := 0
	var dlCurSum int64

	for _, it := range pullItems {
		if it.Status != "done" {
			allPullDone = false
		}
		cur := it.DLCurrent
		if (it.Status == "done" || it.Status == "extracting") && it.DLTotal > cur {
			cur = it.DLTotal
		}
		if cur > 0 || it.Status == "downloading" || it.Status == "extracting" || it.Status == "done" {
			anyActive = true
		}
		dlCurSum += cur
		if it.DLTotal > 0 {
			knownSizeSum += it.DLTotal
			knownSizeCount++
		}
	}

	if !anyActive {
		return 0, 0, 0
	}

	downloadedBytes = dlCurSum
	totalBytes = knownSizeSum
	if downloadedBytes > totalBytes {
		totalBytes = downloadedBytes
	}

	if allPullDone {
		return downloadedBytes, totalBytes, 85
	}

	unknownWeight := 1.0
	if knownSizeCount > 0 {
		unknownWeight = float64(knownSizeSum) / float64(knownSizeCount)
	}

	var weightedDone, weightTotal float64
	for _, it := range pullItems {
		w := float64(it.DLTotal)
		if w <= 0 {
			w = unknownWeight
		}
		sp := float64(it.StagePct)
		if sp < 0 {
			sp = 0
		}
		if sp > 100 {
			sp = 100
		}
		var frac float64
		switch it.Status {
		case "done":
			frac = 1.0
		case "extracting":
			frac = 0.70 + 0.30*(sp/100.0)
		case "downloading":
			frac = 0.70 * (sp / 100.0)
		default:
			frac = 0.0
		}
		weightedDone += w * frac
		weightTotal += w
	}

	if weightTotal <= 0 {
		return downloadedBytes, totalBytes, 0
	}
	ratio := weightedDone / weightTotal
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	pct := int(math.Round(ratio * 85.0))
	if pct >= 85 && !allPullDone {
		pct = 84
	}
	return downloadedBytes, totalBytes, pct
}

type composeLayerMeta struct {
	cached    bool
	dlCurrent int64
	dlTotal   int64
}

// ComposeStreamParser 解析 docker compose pull / up -d 或群晖 build_stream 的实时流式输出，
// 维护有序镜像层进度切片、阶段状态与致命错误检测，同时保留未修改的原始终端块供 @xterm/xterm 渲染。
type ComposeStreamParser struct {
	pending    string
	order      []string
	layers     map[string]*LayerProgress
	meta       map[string]*composeLayerMeta
	layerSizes map[string]int64
	phase      string
	statusText string
	fatalErr   string
}

// NewComposeStreamParser 创建流式输出解析器。
func NewComposeStreamParser() *ComposeStreamParser {
	return NewComposeStreamParserWithSizes(nil)
}

// NewComposeStreamParserWithSizes 创建携带预读镜像层真实体积字典的流式输出解析器。
func NewComposeStreamParserWithSizes(layerSizes map[string]int64) *ComposeStreamParser {
	return &ComposeStreamParser{
		layers:     make(map[string]*LayerProgress),
		meta:       make(map[string]*composeLayerMeta),
		layerSizes: layerSizes,
		phase:      "pulling",
		statusText: "Pulling images",
	}
}

// Feed 喂入一段原始字节流块，返回包含当前各层进度与原始 LogChunk 的 PullProgress。
func (p *ComposeStreamParser) Feed(rawChunk string) PullProgress {
	if rawChunk != "" {
		buf := p.pending + rawChunk
		start := 0
		for i := 0; i < len(buf); i++ {
			if buf[i] == '\r' || buf[i] == '\n' {
				if i > start {
					p.parseLine(buf[start:i])
				}
				start = i + 1
			}
		}
		p.pending = buf[start:]
	}
	return p.Snapshot(rawChunk)
}

// Flush 处理流结束时残留的最后一行（若无换行符结尾）。
func (p *ComposeStreamParser) Flush() {
	if p.pending != "" {
		p.parseLine(p.pending)
		p.pending = ""
	}
}

// FatalError 返回流中检测到的致命守护进程/仓库错误行（若无则为空串）。
func (p *ComposeStreamParser) FatalError() string {
	return p.fatalErr
}

// Snapshot 生成当前进度快照并附带指定的原始 LogChunk。
func (p *ComposeStreamParser) Snapshot(rawChunk string) PullProgress {
	totalLayers := len(p.order)
	completedLayers := 0

	layersOut := make([]LayerProgress, 0, totalLayers)
	inputs := make([]WeightedLayerInput, 0, totalLayers)
	for _, id := range p.order {
		lp := p.layers[id]
		if lp == nil {
			continue
		}
		layersOut = append(layersOut, *lp)
		if lp.Status == "done" {
			completedLayers++
		}
		mt := p.meta[id]
		if mt == nil {
			mt = &composeLayerMeta{dlCurrent: lp.Current, dlTotal: lp.Total}
		}
		inputs = append(inputs, WeightedLayerInput{
			Status:    lp.Status,
			Cached:    mt.cached,
			DLCurrent: mt.dlCurrent,
			DLTotal:   mt.dlTotal,
			StagePct:  lp.Percent,
		})
	}

	downloadedBytes, totalBytes, pct := ComputeWeightedPullProgress(inputs)
	if p.phase == "recreating" {
		pct = 90
	}

	return PullProgress{
		Phase:           p.phase,
		StatusText:      p.statusText,
		TotalLayers:     totalLayers,
		CompletedLayers: completedLayers,
		DownloadedBytes: downloadedBytes,
		TotalBytes:      totalBytes,
		Percent:         pct,
		Layers:          layersOut,
		LogChunk:        rawChunk,
	}
}

func (p *ComposeStreamParser) parseLine(rawLine string) {
	clean := strings.TrimSpace(StripANSI(rawLine))
	if clean == "" {
		return
	}

	lower := strings.ToLower(clean)
	// 检测明显的守护进程或仓库拉取致命错误
	if p.fatalErr == "" {
		for _, marker := range []string{
			"error response from daemon:",
			"pull access denied",
			"unauthorized:",
			"manifest unknown",
			"no matching manifest",
			"failed to solve:",
		} {
			if strings.Contains(lower, marker) {
				p.fatalErr = clean
				break
			}
		}
	}

	if m := reComposeLayer.FindStringSubmatch(clean); m != nil {
		id := m[1]
		rest := strings.TrimSpace(m[2])
		if p.applyLayerUpdate(id, rest) {
			return
		}
	}

	// 识别容器重建阶段行（如 "Container web-1 Recreating" / "Container web-1 Started"）
	if strings.Contains(lower, "container ") &&
		(strings.Contains(lower, "recreating") ||
			strings.Contains(lower, "recreated") ||
			strings.Contains(lower, "creating") ||
			strings.Contains(lower, "created") ||
			strings.Contains(lower, "starting") ||
			strings.Contains(lower, "started")) {
		p.phase = "recreating"
		p.statusText = clean
		// 进入重建阶段时将所有已知层标记为 done
		for _, id := range p.order {
			if lp := p.layers[id]; lp != nil && lp.Status != "done" {
				lp.Status = "done"
				if lp.Total > 0 {
					lp.Current = lp.Total
				}
				lp.Percent = 100
			}
			if mt := p.meta[id]; mt != nil && !mt.cached && mt.dlTotal > 0 {
				mt.dlCurrent = mt.dlTotal
			}
		}
		return
	}

	if strings.Contains(lower, "pulling ") {
		p.statusText = clean
	}
}

func (p *ComposeStreamParser) applyLayerUpdate(id, rest string) bool {
	rl := strings.ToLower(rest)
	var status string
	isCached := false
	switch {
	case strings.HasPrefix(rl, "waiting"), strings.HasPrefix(rl, "pulling fs layer"):
		status = "waiting"
	case strings.HasPrefix(rl, "downloading"):
		status = "downloading"
	case strings.HasPrefix(rl, "verifying checksum"), strings.HasPrefix(rl, "download complete"):
		status = "downloading"
	case strings.HasPrefix(rl, "extracting"):
		status = "extracting"
	case strings.HasPrefix(rl, "already exists"):
		status = "done"
		isCached = true
	case strings.HasPrefix(rl, "pull complete"), strings.HasPrefix(rl, "pulled"):
		status = "done"
	default:
		return false
	}

	lp, exists := p.layers[id]
	if !exists {
		knownSz := LookupLayerSize(p.layerSizes, id)
		lp = &LayerProgress{ID: id, Status: "waiting", Total: knownSz}
		p.layers[id] = lp
		p.meta[id] = &composeLayerMeta{dlTotal: knownSz}
		p.order = append(p.order, id)
	}
	mt := p.meta[id]
	if mt == nil {
		mt = &composeLayerMeta{dlTotal: lp.Total}
		p.meta[id] = mt
	}

	cur, tot, hasBytes := parseByteProgress(rest)

	switch status {
	case "waiting":
		if lp.Status == "" || lp.Status == "waiting" {
			lp.Status = "waiting"
			lp.Percent = 0
		}
	case "downloading":
		if lp.Status != "done" && lp.Status != "extracting" {
			lp.Status = "downloading"
			if hasBytes {
				if tot > mt.dlTotal {
					mt.dlTotal = tot
				}
				if cur > mt.dlCurrent {
					mt.dlCurrent = cur
				}
				lp.Total = mt.dlTotal
				lp.Current = mt.dlCurrent
			}
			if strings.HasPrefix(rl, "verifying checksum") || strings.HasPrefix(rl, "download complete") {
				if mt.dlTotal > 0 {
					mt.dlCurrent = mt.dlTotal
					lp.Current = mt.dlTotal
				}
				lp.Percent = 100
			} else if mt.dlTotal > 0 {
				pct := int(mt.dlCurrent * 100 / mt.dlTotal)
				if pct > 99 {
					pct = 99
				}
				if pct > lp.Percent {
					lp.Percent = pct
				}
			}
		}
	case "extracting":
		if lp.Status != "done" {
			prevStatus := lp.Status
			lp.Status = "extracting"
			if mt.dlTotal > 0 {
				mt.dlCurrent = mt.dlTotal
			}
			if hasBytes && tot > 0 {
				lp.Current = cur
				lp.Total = tot
				pct := int(cur * 100 / tot)
				if pct > 99 {
					pct = 99
				}
				lp.Percent = pct
			} else if prevStatus != "extracting" {
				lp.Percent = 50
			}
		}
	case "done":
		lp.Status = "done"
		if isCached {
			mt.cached = true
			mt.dlCurrent = 0
			mt.dlTotal = 0
			lp.Current = 0
			lp.Total = 0
		} else {
			if mt.dlTotal > 0 {
				mt.dlCurrent = mt.dlTotal
				lp.Total = mt.dlTotal
				lp.Current = mt.dlTotal
			} else if lp.Total > 0 {
				lp.Current = lp.Total
			}
		}
		lp.Percent = 100
	}

	p.phase = "pulling"
	p.statusText = id + ": " + status
	return true
}

func parseByteProgress(s string) (cur int64, tot int64, ok bool) {
	m := reBytePair.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	cVal, err1 := strconv.ParseFloat(m[1], 64)
	tVal, err2 := strconv.ParseFloat(m[3], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return int64(cVal * unitMultiplier(m[2])), int64(tVal * unitMultiplier(m[4])), true
}

func unitMultiplier(unit string) float64 {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "B":
		return 1
	case "KB":
		return 1000
	case "KIB":
		return 1024
	case "MB":
		return 1000 * 1000
	case "MIB":
		return 1024 * 1024
	case "GB":
		return 1000 * 1000 * 1000
	case "GIB":
		return 1024 * 1024 * 1024
	case "TB":
		return 1000 * 1000 * 1000 * 1000
	case "TIB":
		return 1024 * 1024 * 1024 * 1024
	default:
		return 1
	}
}
