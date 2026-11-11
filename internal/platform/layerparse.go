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

// ComposeStreamParser 解析 docker compose pull / up -d 或群晖 build_stream 的实时流式输出，
// 维护有序镜像层进度切片、阶段状态与致命错误检测，同时保留未修改的原始终端块供 @xterm/xterm 渲染。
type ComposeStreamParser struct {
	pending    string
	order      []string
	layers     map[string]*LayerProgress
	phase      string
	statusText string
	fatalErr   string
}

// NewComposeStreamParser 创建流式输出解析器。
func NewComposeStreamParser() *ComposeStreamParser {
	return &ComposeStreamParser{
		layers:     make(map[string]*LayerProgress),
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
	var downloadedBytes, totalBytes int64
	var fracSum float64

	layersOut := make([]LayerProgress, 0, totalLayers)
	for _, id := range p.order {
		lp := p.layers[id]
		if lp == nil {
			continue
		}
		layersOut = append(layersOut, *lp)
		downloadedBytes += lp.Current
		totalBytes += lp.Total
		switch lp.Status {
		case "done":
			completedLayers++
			fracSum += 1.0
		case "extracting":
			fracSum += 0.55 + 0.40*(float64(lp.Percent)/100.0)
		case "downloading":
			fracSum += 0.55 * (float64(lp.Percent) / 100.0)
		}
	}

	pct := 10
	if p.phase == "recreating" {
		pct = 85
	} else if totalLayers > 0 {
		ratio := fracSum / float64(totalLayers)
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		pct = 10 + int(math.Round(ratio*70.0)) // pulling 区间映射到 10%..80%
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
	switch {
	case strings.HasPrefix(rl, "waiting"), strings.HasPrefix(rl, "pulling fs layer"):
		status = "waiting"
	case strings.HasPrefix(rl, "downloading"):
		status = "downloading"
	case strings.HasPrefix(rl, "verifying checksum"), strings.HasPrefix(rl, "download complete"):
		status = "downloading"
	case strings.HasPrefix(rl, "extracting"):
		status = "extracting"
	case strings.HasPrefix(rl, "pull complete"), strings.HasPrefix(rl, "already exists"), strings.HasPrefix(rl, "pulled"):
		status = "done"
	default:
		return false
	}

	lp, exists := p.layers[id]
	if !exists {
		lp = &LayerProgress{ID: id, Status: "waiting"}
		p.layers[id] = lp
		p.order = append(p.order, id)
	}

	cur, tot, hasBytes := parseByteProgress(rest)
	if hasBytes {
		if tot > lp.Total {
			lp.Total = tot
		}
		if cur > lp.Current || status != lp.Status {
			lp.Current = cur
		}
	}

	switch status {
	case "waiting":
		if lp.Status == "" || lp.Status == "waiting" {
			lp.Status = "waiting"
			lp.Percent = 0
		}
	case "downloading":
		if lp.Status != "done" && lp.Status != "extracting" {
			lp.Status = "downloading"
			if strings.HasPrefix(rl, "verifying checksum") || strings.HasPrefix(rl, "download complete") {
				if lp.Total > 0 {
					lp.Current = lp.Total
				}
				lp.Percent = 100
			} else if lp.Total > 0 {
				pct := int(lp.Current * 100 / lp.Total)
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
			if hasBytes && tot > 0 {
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
		if lp.Total > 0 {
			lp.Current = lp.Total
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
