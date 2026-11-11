package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
	"gopkg.in/yaml.v3"
)

// HasLocalImage 检查目标主机本地是否仍保留指定 ImageID 或标签镜像。
func (a *Adapter) HasLocalImage(ctx context.Context, imageIDOrRef string) bool {
	if strings.TrimSpace(imageIDOrRef) == "" {
		return false
	}
	var ii imageInspect
	return a.getJSON(ctx, "/images/"+url.PathEscape(imageIDOrRef)+"/json", &ii) == nil && ii.ID != ""
}

// UpdateService 执行单服务镜像拉取、冲突检测、按原配置重建与状态核验（立项文档 §7 / §8 / §9）。
// 返回的 UpdateError.Message 为 i18n.EngineDetail 编码，由上层按请求语言渲染（审计 I1）。
func (a *Adapter) UpdateService(
	ctx context.Context,
	req platform.UpdateRequest,
	onProgress func(platform.PullProgress),
) (*platform.UpdateResult, error) {
	if req.Source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: i18n.EngineDetail(i18n.MsgDockerVendorReadonly),
		}
	}
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return nil, &platform.UpdateError{
			Code:    "capability_unsupported",
			Message: i18n.EngineDetail(i18n.MsgDockerNoExec),
		}
	}
	emit := func(p platform.PullProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	var logs []string
	addLog := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...)))
	}

	// 1. Pre-check & 冲突检测（立项文档 §7.1 / §7.2 / A15）
	emit(platform.PullProgress{
		Phase:      "precheck",
		StatusText: "Checking target container & configuration for conflicts",
		Percent:    5,
		LogChunk:   "Checking target container & configuration for conflicts...\r\n",
	})
	targetContainerID, rawInspect, wasRunning, err := a.findTargetContainer(ctx, req)
	if err != nil {
		return nil, &platform.UpdateError{Code: "precheck_failed", Message: i18n.DetailOf(err)}
	}
	curImageID, _ := rawInspect["Image"].(string)
	if req.ExpectedImageID != "" && curImageID != "" &&
		!strings.HasPrefix(curImageID, req.ExpectedImageID) && !strings.HasPrefix(req.ExpectedImageID, curImageID) {
		return nil, &platform.UpdateError{
			Code:    "external_conflict",
			Message: i18n.EngineDetail(i18n.MsgDockerImageChanged, shortID(req.ExpectedImageID), shortID(curImageID)),
		}
	}
	addLog("precheck ok: container=%s image=%s wasRunning=%v", shortID(targetContainerID), shortID(curImageID), wasRunning)

	// 若为 Compose / DSM 项目，提前记录 Compose YAML 的原始内容与 SHA256，用于冲突保护与失败回写
	var (
		composePath      string
		composeBytes     []byte
		origComposeSHA   string // 更新前的 SHA256（L11：old_compose_sha256 记录的是更新前的值）
		writtenSHA       string // 写入新内容后的 SHA256（为空表示未改写文件）
		composeRewritten bool
	)
	if (req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject) &&
		req.Project != nil && len(req.Project.ConfigFiles) > 0 {
		for _, cf := range req.Project.ConfigFiles {
			full := resolvePath(req.Project.WorkingDir, cf)
			if b, err := a.tr.ReadFile(ctx, full); err == nil && len(b) > 0 {
				composePath = full
				composeBytes = b
				sum := sha256.Sum256(b)
				origComposeSHA = hex.EncodeToString(sum[:])
				break
			}
		}
	}

	// restoreCompose 在 Compose 文件已改写、但后续重建或校验失败时写回原内容（审计 M5），返回附带回写结果的错误详情。
	restoreCompose := func(detail string) string {
		if !composeRewritten {
			return detail
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if err := execTr.WriteFileAtomic(rctx, composePath, composeBytes, writtenSHA); err != nil {
			addLog("restore compose file %s failed: %v", composePath, err)
			return i18n.EngineDetail(i18n.MsgDockerComposeRestoreErr, detail, err.Error())
		}
		composeRewritten = false
		addLog("restored original compose file %s", composePath)
		return i18n.EngineDetail(i18n.MsgDockerComposeRestored, detail)
	}

	// 2. 拉取目标镜像并流式解析分层进度（立项文档 §7.1 / §7.3 / A04 / 附录 E.1）
	emit(platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Pulling image " + req.TargetImageRef,
		Percent:    10,
		LogChunk:   fmt.Sprintf("Pulling image %s ...\r\n", req.TargetImageRef),
	})
	addLog("pulling image: %s", req.TargetImageRef)
	newImageID, newRepoDigest, err := a.pullImageStream(ctx, execTr, req.TargetImageRef, req.RegistryAuthB64, emit)
	if err != nil {
		return nil, &platform.UpdateError{Code: "pull_failed", Message: i18n.DetailOf(err)}
	}
	addLog("image pulled: id=%s digest=%s", shortID(newImageID), newRepoDigest)

	// 3. 重建指定服务（立项文档 §7.1 / §7.2 / A13）
	emit(platform.PullProgress{
		Phase:      "recreating",
		StatusText: "Recreating service " + req.ServiceName,
		Percent:    85,
		LogChunk:   fmt.Sprintf("Recreating service %s ...\r\n", req.ServiceName),
	})

	usedComposeCLI := false
	if (req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject) &&
		composePath != "" && req.Project != nil {
		// 若镜像引用发生变化（如版本标签升级或回退），原子更新 YAML 中对应服务的 image 行
		if req.OldImageRef != "" && req.TargetImageRef != "" && req.OldImageRef != req.TargetImageRef {
			updatedYAML, changed := replaceServiceImageInYAML(composeBytes, req.ServiceName, req.TargetImageRef)
			if changed {
				if err := execTr.WriteFileAtomic(ctx, composePath, updatedYAML, origComposeSHA); err != nil {
					if errors.Is(err, transport.ErrFileConflict) {
						return nil, &platform.UpdateError{
							Code:    "external_conflict",
							Message: i18n.EngineDetail(i18n.MsgDockerComposeChanged),
						}
					}
					return nil, &platform.UpdateError{
						Code:    "recreate_failed",
						Message: i18n.EngineDetail(i18n.MsgDockerComposeWrite, composePath, err.Error()),
					}
				}
				sum := sha256.Sum256(updatedYAML)
				writtenSHA = hex.EncodeToString(sum[:])
				composeRewritten = true
				addLog("updated compose file %s (sha256=%s)", composePath, writtenSHA[:12])
			}
		}

		var out string
		var cErr error
		if streamTr, ok := execTr.(transport.StreamingComposeTransport); ok {
			out, cErr = streamTr.ExecComposeServiceStream(
				ctx,
				req.Project.WorkingDir,
				req.Project.ConfigFiles,
				req.Project.EnvFiles,
				req.Project.Name,
				req.ServiceName,
				wasRunning,
				func(chunk string) {
					emit(platform.PullProgress{
						Phase:      "recreating",
						StatusText: "Recreating service " + req.ServiceName,
						Percent:    88,
						LogChunk:   chunk,
					})
				},
			)
		} else {
			out, cErr = execTr.ExecComposeService(
				ctx,
				req.Project.WorkingDir,
				req.Project.ConfigFiles,
				req.Project.EnvFiles,
				req.Project.Name,
				req.ServiceName,
				wasRunning,
			)
			if out != "" {
				emit(platform.PullProgress{
					Phase:      "recreating",
					StatusText: "Recreating service " + req.ServiceName,
					Percent:    88,
					LogChunk:   out + "\r\n",
				})
			}
		}
		if cErr == nil {
			usedComposeCLI = true
			if out != "" {
				addLog("docker compose output: %s", out)
			}
		} else {
			addLog("docker compose cli fallback to engine api (%v)", cErr)
		}
	}

	if !usedComposeCLI {
		newCID, err := a.recreateContainerViaEngine(ctx, execTr, targetContainerID, rawInspect, req.TargetImageRef, wasRunning)
		if err != nil {
			return nil, &platform.UpdateError{Code: "recreate_failed", Message: restoreCompose(i18n.DetailOf(err))}
		}
		addLog("recreated container via Engine API: %s", shortID(newCID))
		emit(platform.PullProgress{
			Phase:      "recreating",
			StatusText: "Container recreated: " + shortID(newCID),
			Percent:    90,
			LogChunk:   fmt.Sprintf("Container recreated via Docker Engine API (%s)\r\n", shortID(newCID)),
		})
	}

	// 4. 校验运行状态与实际运行镜像切换（立项文档 §4.1 / §7.1 / §7.2 / A03）
	emit(platform.PullProgress{
		Phase:      "verifying",
		StatusText: "Verifying container state & switched image",
		Percent:    95,
		LogChunk:   "Verifying container state & switched image...\r\n",
	})
	res, err := a.verifyUpdatedService(ctx, req, newImageID, newRepoDigest, wasRunning)
	if err != nil {
		return nil, &platform.UpdateError{Code: "verify_failed", Message: restoreCompose(i18n.DetailOf(err))}
	}
	res.ComposePath = composePath
	res.ComposeSHA256 = origComposeSHA
	addLog("verified ok: container=%s state=%s health=%s image=%s",
		shortID(res.NewContainerID), res.ContainerState, res.HealthStatus, shortID(res.NewImageID))
	res.Logs = logs

	emit(platform.PullProgress{
		Phase:      "done",
		StatusText: "Update completed",
		Percent:    100,
	})
	return res, nil
}

type layerStat struct {
	id         string
	status     string // waiting | downloading | extracting | done
	lastRawSt  string
	current    int64
	total      int64
	pct        int
	done       bool
}

func formatBytesShort(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.2f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.2f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f kB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func (a *Adapter) pullImageStream(
	ctx context.Context,
	execTr transport.ExecutableTransport,
	targetRef string,
	registryAuthB64 string,
	emit func(platform.PullProgress),
) (imageID, repoDigest string, err error) {
	fromImg, tag := splitPullImageAndTag(targetRef)
	q := url.Values{}
	q.Set("fromImage", fromImg)
	if tag != "" {
		q.Set("tag", tag)
	}
	headers := map[string]string{}
	if registryAuthB64 != "" {
		headers["X-Registry-Auth"] = registryAuthB64
	}
	resp, err := execTr.RequestDocker(ctx, http.MethodPost, "/images/create?"+q.Encode(), nil, headers)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", i18n.NewEngineError(i18n.MsgDockerPullFailed, dockerHTTPErr(resp.StatusCode, strings.TrimSpace(string(body))))
	}

	var layerOrder []string
	layers := make(map[string]*layerStat)
	prevWasOverwrite := false
	dec := json.NewDecoder(resp.Body)
	for {
		var ev struct {
			ID             string `json:"id"`
			Status         string `json:"status"`
			Error          string `json:"error"`
			ProgressDetail struct {
				Current int64 `json:"current"`
				Total   int64 `json:"total"`
			} `json:"progressDetail"`
		}
		if err := dec.Decode(&ev); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", "", err
		}
		if ev.Error != "" {
			return "", "", errors.New(ev.Error)
		}

		stLower := strings.ToLower(ev.Status)
		isLayerEv := ev.ID != "" && len(ev.ID) <= 16 && !strings.Contains(ev.ID, ":") &&
			(strings.Contains(stLower, "waiting") ||
				strings.Contains(stLower, "pulling fs layer") ||
				strings.Contains(stLower, "downloading") ||
				strings.Contains(stLower, "extracting") ||
				strings.Contains(stLower, "verifying") ||
				strings.Contains(stLower, "download complete") ||
				strings.Contains(stLower, "pull complete") ||
				strings.Contains(stLower, "already exists"))

		var logChunk string
		if isLayerEv {
			ls, ok := layers[ev.ID]
			if !ok {
				ls = &layerStat{id: ev.ID, status: "waiting"}
				layers[ev.ID] = ls
				layerOrder = append(layerOrder, ev.ID)
			}
			if ev.ProgressDetail.Total > ls.total {
				ls.total = ev.ProgressDetail.Total
			}
			if ev.ProgressDetail.Current > ls.current {
				ls.current = ev.ProgressDetail.Current
			}

			switch {
			case strings.Contains(stLower, "pull complete") || strings.Contains(stLower, "already exists"):
				ls.status = "done"
				ls.done = true
				if ls.total > 0 {
					ls.current = ls.total
				}
				ls.pct = 100
				prefix := ""
				if prevWasOverwrite {
					prefix = "\r\x1b[2K"
					prevWasOverwrite = false
				}
				if ls.lastRawSt != ev.Status {
					logChunk = fmt.Sprintf("%s\x1b[32m✔\x1b[0m \x1b[36m%s\x1b[0m: %s\r\n", prefix, ev.ID, ev.Status)
				}
			case strings.Contains(stLower, "extracting"):
				if !ls.done {
					ls.status = "extracting"
					if ev.ProgressDetail.Total > 0 {
						p := int(ev.ProgressDetail.Current * 100 / ev.ProgressDetail.Total)
						if p > 99 {
							p = 99
						}
						ls.pct = p
						logChunk = fmt.Sprintf("\r\x1b[2K\x1b[36m%s\x1b[0m: Extracting %s / %s (%d%%)", ev.ID, formatBytesShort(ev.ProgressDetail.Current), formatBytesShort(ev.ProgressDetail.Total), p)
						prevWasOverwrite = true
					} else if ls.pct < 50 {
						ls.pct = 50
					}
				}
			case strings.Contains(stLower, "downloading") || strings.Contains(stLower, "verifying") || strings.Contains(stLower, "download complete"):
				if !ls.done && ls.status != "extracting" {
					ls.status = "downloading"
					if strings.Contains(stLower, "download complete") || strings.Contains(stLower, "verifying") {
						if ls.total > 0 {
							ls.current = ls.total
						}
						ls.pct = 100
						if ls.lastRawSt != ev.Status {
							prefix := ""
							if prevWasOverwrite {
								prefix = "\r\x1b[2K"
								prevWasOverwrite = false
							}
							logChunk = fmt.Sprintf("%s\x1b[36m%s\x1b[0m: %s\r\n", prefix, ev.ID, ev.Status)
						}
					} else if ls.total > 0 {
						p := int(ls.current * 100 / ls.total)
						if p > 99 {
							p = 99
						}
						if p > ls.pct {
							ls.pct = p
						}
						logChunk = fmt.Sprintf("\r\x1b[2K\x1b[36m%s\x1b[0m: Downloading %s / %s (%d%%)", ev.ID, formatBytesShort(ls.current), formatBytesShort(ls.total), ls.pct)
						prevWasOverwrite = true
					}
				}
			default:
				if ls.lastRawSt != ev.Status {
					prefix := ""
					if prevWasOverwrite {
						prefix = "\r\x1b[2K"
						prevWasOverwrite = false
					}
					logChunk = fmt.Sprintf("%s\x1b[36m%s\x1b[0m: %s\r\n", prefix, ev.ID, ev.Status)
				}
			}
			ls.lastRawSt = ev.Status
		} else if ev.Status != "" {
			prefix := ""
			if prevWasOverwrite {
				prefix = "\r\x1b[2K"
				prevWasOverwrite = false
			}
			if ev.ID != "" {
				logChunk = fmt.Sprintf("%s%s: %s\r\n", prefix, ev.ID, ev.Status)
			} else {
				logChunk = fmt.Sprintf("%s%s\r\n", prefix, ev.Status)
			}
		}

		totalLayers := len(layerOrder)
		completedLayers := 0
		var downloadedBytes, totalBytes int64
		layersOut := make([]platform.LayerProgress, 0, totalLayers)
		for _, id := range layerOrder {
			l := layers[id]
			if l.done {
				completedLayers++
			}
			downloadedBytes += l.current
			totalBytes += l.total
			layersOut = append(layersOut, platform.LayerProgress{
				ID:      l.id,
				Status:  l.status,
				Current: l.current,
				Total:   l.total,
				Percent: l.pct,
			})
		}

		rawPct := 0
		if totalBytes > 0 {
			rawPct = int(downloadedBytes * 100 / totalBytes)
		} else if totalLayers > 0 {
			rawPct = completedLayers * 100 / totalLayers
		}
		if rawPct > 100 {
			rawPct = 100
		}
		pct := 10 + rawPct*70/100
		if pct > 80 {
			pct = 80
		}
		emit(platform.PullProgress{
			Phase:           "pulling",
			StatusText:      ev.Status,
			TotalLayers:     totalLayers,
			CompletedLayers: completedLayers,
			DownloadedBytes: downloadedBytes,
			TotalBytes:      totalBytes,
			Percent:         pct,
			Layers:          layersOut,
			LogChunk:        logChunk,
		})
	}

	var ii imageInspect
	if err := a.getJSON(ctx, "/images/"+url.PathEscape(targetRef)+"/json", &ii); err != nil {
		return "", "", i18n.NewEngineError(i18n.MsgDockerInspectPulled, err.Error())
	}
	return ii.ID, pickDigestForRepo(fromImg, ii.RepoDigests), nil
}

// pickDigestForRepo 优先返回与目标仓库匹配的 RepoDigest，找不到时退回第一个 sha256 摘要。
func pickDigestForRepo(repo string, repoDigests []string) string {
	want := normalizeRepoName(repo)
	first := ""
	for _, rd := range repoDigests {
		name, d, ok := strings.Cut(rd, "@")
		if !ok || !strings.HasPrefix(d, "sha256:") {
			continue
		}
		if first == "" {
			first = d
		}
		if normalizeRepoName(name) == want {
			return d
		}
	}
	return first
}

func normalizeRepoName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimPrefix(s, "library/")
	return s
}

func splitPullImageAndTag(ref string) (fromImage, tag string) {
	s := strings.TrimSpace(ref)
	if at := strings.Index(s, "@"); at >= 0 {
		return s[:at], s[at+1:]
	}
	lastSlash := strings.LastIndex(s, "/")
	colon := strings.LastIndex(s, ":")
	if colon > lastSlash {
		return s[:colon], s[colon+1:]
	}
	return s, "latest"
}

// ReplaceServiceImageInYAML 精确替换 Compose YAML 中指定服务块下的 image: 行，完全保留原文件注释、缩进与其他服务配置。
func ReplaceServiceImageInYAML(raw []byte, serviceName, newImage string) ([]byte, bool) {
	return replaceServiceImageInYAML(raw, serviceName, newImage)
}

// replaceServiceImageInYAML 精确替换 Compose YAML 中指定服务块下的 image: 行，完全保留原文件注释、缩进与其他服务配置。
func replaceServiceImageInYAML(raw []byte, serviceName, newImage string) ([]byte, bool) {
	lines := strings.Split(string(raw), "\n")
	inServices := false
	servicesIndent := -1
	inTargetSvc := false
	targetSvcIndent := -1
	reImg := regexp.MustCompile(`^(\s*image\s*:\s*)([^\s#]+)(.*)$`)

	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := countLeadingSpaces(line)

		if !inServices {
			if trimmed == "services:" || strings.HasPrefix(trimmed, "services:") {
				inServices = true
				servicesIndent = indent
			}
			continue
		}

		// 若退出了 services: 顶级块
		if indent <= servicesIndent && !strings.HasPrefix(trimmed, "#") {
			inServices = false
			inTargetSvc = false
			continue
		}

		if inTargetSvc && indent <= targetSvcIndent {
			inTargetSvc = false
		}

		if !inTargetSvc {
			// 匹配服务名行，如 "  web:"
			if strings.HasPrefix(trimmed, serviceName+":") {
				rest := strings.TrimSpace(strings.TrimPrefix(trimmed, serviceName+":"))
				if rest == "" || strings.HasPrefix(rest, "#") {
					inTargetSvc = true
					targetSvcIndent = indent
				}
			}
			continue
		}

		// 在目标服务块内查找 image:
		cr := ""
		lineNoCR := line
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			lineNoCR = strings.TrimSuffix(line, "\r")
		}
		if m := reImg.FindStringSubmatch(lineNoCR); m != nil {
			lines[i] = m[1] + newImage + m[3] + cr
			return []byte(strings.Join(lines, "\n")), true
		}
	}
	return raw, false
}

func countLeadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
		} else if r == '\t' {
			n += 2
		} else {
			break
		}
	}
	return n
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func readBodyString(r io.Reader) string {
	var b bytes.Buffer
	_, _ = io.Copy(&b, io.LimitReader(r, 4096))
	return strings.TrimSpace(b.String())
}

// ValidateComposeYAML 校验 Compose YAML 语法并提取其中的 services.<name>.image 声明映射。
func ValidateComposeYAML(rawYAML string) (map[string]string, error) {
	if strings.TrimSpace(rawYAML) == "" {
		return nil, i18n.NewEngineError(i18n.MsgComposeInvalidYAML, "empty content")
	}
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(rawYAML), &doc); err != nil {
		return nil, i18n.NewEngineError(i18n.MsgComposeInvalidYAML, err.Error())
	}
	if len(doc.Services) == 0 {
		return nil, i18n.NewEngineError(i18n.MsgComposeInvalidYAML, "missing services map")
	}
	images := make(map[string]string, len(doc.Services))
	for svcName, svcMap := range doc.Services {
		if svcMap == nil {
			continue
		}
		if img, ok := svcMap["image"].(string); ok && strings.TrimSpace(img) != "" {
			images[svcName] = strings.TrimSpace(img)
		}
	}
	return images, nil
}

func resolvePrimaryComposeFile(project *platform.ProjectInfo) string {
	if project == nil {
		return ""
	}
	if len(project.ConfigFiles) > 0 && strings.TrimSpace(project.ConfigFiles[0]) != "" {
		return resolvePath(project.WorkingDir, strings.TrimSpace(project.ConfigFiles[0]))
	}
	if strings.TrimSpace(project.WorkingDir) != "" {
		return resolvePath(project.WorkingDir, "compose.yaml")
	}
	return ""
}

// GetProjectCompose 读取 Compose 项目的当前主 YAML 文件内容。
func (a *Adapter) GetProjectCompose(ctx context.Context, project *platform.ProjectInfo) (string, error) {
	p := resolvePrimaryComposeFile(project)
	if p == "" {
		return "", i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}
	b, err := a.tr.ReadFile(ctx, p)
	if err != nil && len(project.ConfigFiles) == 0 && project.WorkingDir != "" {
		// 若未记录 ConfigFiles，依次尝试常见默认文件名
		for _, alt := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml"} {
			if b2, err2 := a.tr.ReadFile(ctx, resolvePath(project.WorkingDir, alt)); err2 == nil {
				return string(b2), nil
			}
		}
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UpdateProjectCompose 备份原 Compose 文件、写入新版 YAML 并执行拉取与重建；失败时自动写回原文件并恢复原容器（附录 D.4）。
func (a *Adapter) UpdateProjectCompose(
	ctx context.Context,
	appName string,
	project *platform.ProjectInfo,
	newYAML string,
	onProgress func(platform.PullProgress),
) error {
	if _, err := ValidateComposeYAML(newYAML); err != nil {
		return err
	}
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return &platform.UpdateError{
			Code:    "capability_unsupported",
			Message: i18n.EngineDetail(i18n.MsgDockerNoExec),
		}
	}
	composePath := resolvePrimaryComposeFile(project)
	if composePath == "" {
		return i18n.NewEngineError(i18n.MsgComposeNotEditable)
	}

	origBytes, err := a.tr.ReadFile(ctx, composePath)
	if err != nil {
		return err
	}
	origSum := sha256.Sum256(origBytes)
	origSHA := hex.EncodeToString(origSum[:])

	emit := func(p platform.PullProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}
	emit(platform.PullProgress{
		Phase:      "precheck",
		StatusText: "Writing updated compose configuration",
		Percent:    5,
		LogChunk:   fmt.Sprintf("Writing updated Compose configuration to %s ...\r\n", composePath),
	})

	newBytes := []byte(newYAML)
	if err := execTr.WriteFileAtomic(ctx, composePath, newBytes, origSHA); err != nil {
		if errors.Is(err, transport.ErrFileConflict) {
			return &platform.UpdateError{
				Code:    "external_conflict",
				Message: i18n.EngineDetail(i18n.MsgDockerComposeChanged),
			}
		}
		return &platform.UpdateError{
			Code:    "recreate_failed",
			Message: i18n.EngineDetail(i18n.MsgDockerComposeWrite, composePath, err.Error()),
		}
	}
	writtenSum := sha256.Sum256(newBytes)
	writtenSHA := hex.EncodeToString(writtenSum[:])

	projName := project.Name
	if projName == "" {
		projName = appName
	}
	cfgFiles := project.ConfigFiles
	if len(cfgFiles) == 0 {
		cfgFiles = []string{composePath}
	}

	parser := platform.NewComposeStreamParser()
	emit(platform.PullProgress{
		Phase:      "pulling",
		StatusText: "Pulling images and recreating Compose project",
		Percent:    10,
		LogChunk:   "Running docker compose pull & up -d --remove-orphans ...\r\n",
	})

	var runErr error
	if streamTr, ok := execTr.(transport.StreamingComposeTransport); ok {
		_, runErr = streamTr.ExecComposeProjectStream(ctx, project.WorkingDir, cfgFiles, project.EnvFiles, projName, true, func(chunk string) {
			emit(parser.Feed(chunk))
		})
		parser.Flush()
	} else {
		var out string
		out, runErr = execTr.ExecComposeProject(ctx, project.WorkingDir, cfgFiles, project.EnvFiles, projName, true)
		if out != "" {
			emit(parser.Feed(out + "\r\n"))
			parser.Flush()
		}
	}
	if runErr != nil {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if rErr := execTr.WriteFileAtomic(rctx, composePath, origBytes, writtenSHA); rErr != nil {
			return &platform.UpdateError{
				Code:    "recreate_failed",
				Message: i18n.EngineDetail(i18n.MsgDockerComposeRestoreErr, runErr.Error(), rErr.Error()),
			}
		}
		_, _ = execTr.ExecComposeProject(rctx, project.WorkingDir, cfgFiles, project.EnvFiles, projName, false)
		return &platform.UpdateError{
			Code:    "recreate_failed",
			Message: i18n.EngineDetail(i18n.MsgDockerComposeRestored, runErr.Error()),
		}
	}

	emit(platform.PullProgress{
		Phase:      "done",
		StatusText: "Compose project updated",
		Percent:    100,
		LogChunk:   "Compose project update completed.\r\n",
	})
	return nil
}
