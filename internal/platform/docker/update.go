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

	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
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
func (a *Adapter) UpdateService(
	ctx context.Context,
	req platform.UpdateRequest,
	onProgress func(platform.PullProgress),
) (*platform.UpdateResult, error) {
	if req.Source == platform.SourceVendorManaged {
		return nil, &platform.UpdateError{
			Code:    "vendor_managed_readonly",
			Message: "vendor managed container cannot be modified directly",
		}
	}
	execTr, ok := a.tr.(transport.ExecutableTransport)
	if !ok {
		return nil, &platform.UpdateError{
			Code:    "capability_unsupported",
			Message: "transport does not support update execution",
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
	emit(platform.PullProgress{Phase: "precheck", StatusText: "Checking target container & configuration for conflicts"})
	targetContainerID, rawInspect, wasRunning, err := a.findTargetContainer(ctx, req)
	if err != nil {
		return nil, &platform.UpdateError{Code: "precheck_failed", Message: err.Error()}
	}
	curImageID, _ := rawInspect["Image"].(string)
	if req.ExpectedImageID != "" && curImageID != "" &&
		!strings.HasPrefix(curImageID, req.ExpectedImageID) && !strings.HasPrefix(req.ExpectedImageID, curImageID) {
		return nil, &platform.UpdateError{
			Code:    "external_conflict",
			Message: fmt.Sprintf("container image changed externally (%s -> %s)", shortID(req.ExpectedImageID), shortID(curImageID)),
		}
	}
	addLog("precheck ok: container=%s image=%s wasRunning=%v", shortID(targetContainerID), shortID(curImageID), wasRunning)

	// 若为 Compose / DSM 项目，提前记录 Compose YAML 的 SHA256 用于冲突保护
	var (
		composePath   string
		composeBytes  []byte
		composeSHA256 string
	)
	if (req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject) &&
		req.Project != nil && len(req.Project.ConfigFiles) > 0 {
		for _, cf := range req.Project.ConfigFiles {
			full := resolvePath(req.Project.WorkingDir, cf)
			if b, err := a.tr.ReadFile(ctx, full); err == nil && len(b) > 0 {
				composePath = full
				composeBytes = b
				sum := sha256.Sum256(b)
				composeSHA256 = hex.EncodeToString(sum[:])
				break
			}
		}
	}

	// 2. 拉取目标镜像并流式解析分层进度（立项文档 §7.1 / §7.3 / A04）
	emit(platform.PullProgress{Phase: "pulling", StatusText: "Pulling image " + req.TargetImageRef})
	addLog("pulling image: %s", req.TargetImageRef)
	newImageID, newRepoDigest, err := a.pullImageStream(ctx, execTr, req.TargetImageRef, req.RegistryAuthB64, emit)
	if err != nil {
		return nil, &platform.UpdateError{Code: "pull_failed", Message: err.Error()}
	}
	addLog("image pulled: id=%s digest=%s", shortID(newImageID), newRepoDigest)

	// 3. 重建指定服务（立项文档 §7.1 / §7.2 / A13）
	emit(platform.PullProgress{
		Phase:      "recreating",
		StatusText: "Recreating service " + req.ServiceName,
		Percent:    100,
	})

	usedComposeCLI := false
	if (req.Source == platform.SourceCompose || req.Source == platform.SourceDSMProject) &&
		composePath != "" && req.Project != nil {
		// 若镜像引用发生变化（如版本标签升级或回退），原子更新 YAML 中对应服务的 image 行
		if req.OldImageRef != "" && req.TargetImageRef != "" && req.OldImageRef != req.TargetImageRef {
			updatedYAML, changed := replaceServiceImageInYAML(composeBytes, req.ServiceName, req.TargetImageRef)
			if changed {
				if err := execTr.WriteFileAtomic(ctx, composePath, updatedYAML, composeSHA256); err != nil {
					if errors.Is(err, transport.ErrFileConflict) {
						return nil, &platform.UpdateError{
							Code:    "external_conflict",
							Message: "Compose YAML was modified externally during image pull",
						}
					}
					return nil, &platform.UpdateError{
						Code:    "recreate_failed",
						Message: fmt.Sprintf("failed to update Compose file %s: %v", composePath, err),
					}
				}
				sum := sha256.Sum256(updatedYAML)
				composeSHA256 = hex.EncodeToString(sum[:])
				addLog("updated compose file %s (sha256=%s)", composePath, composeSHA256[:12])
			}
		}

		out, err := execTr.ExecComposeService(
			ctx,
			req.Project.WorkingDir,
			req.Project.ConfigFiles,
			req.Project.EnvFiles,
			req.Project.Name,
			req.ServiceName,
			wasRunning,
		)
		if err == nil {
			usedComposeCLI = true
			if out != "" {
				addLog("docker compose output: %s", out)
			}
		} else {
			addLog("docker compose cli fallback to engine api (%v)", err)
		}
	}

	if !usedComposeCLI {
		newCID, err := a.recreateContainerViaEngine(ctx, execTr, targetContainerID, rawInspect, req.TargetImageRef, wasRunning)
		if err != nil {
			return nil, &platform.UpdateError{Code: "recreate_failed", Message: err.Error()}
		}
		addLog("recreated container via Engine API: %s", shortID(newCID))
	}

	// 4. 校验运行状态与实际运行镜像切换（立项文档 §4.1 / §7.1 / §7.2 / A03）
	emit(platform.PullProgress{
		Phase:      "verifying",
		StatusText: "Verifying container state & switched image",
		Percent:    100,
	})
	res, err := a.verifyUpdatedService(ctx, req, newImageID, newRepoDigest, wasRunning)
	if err != nil {
		return nil, &platform.UpdateError{Code: "verify_failed", Message: err.Error()}
	}
	res.ComposePath = composePath
	res.ComposeSHA256 = composeSHA256
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
	current int64
	total   int64
	done    bool
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
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	layers := make(map[string]*layerStat)
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
		if ev.ID != "" && len(ev.ID) <= 16 && !strings.Contains(ev.ID, ":") &&
			(strings.Contains(stLower, "pulling fs layer") ||
				strings.Contains(stLower, "downloading") ||
				strings.Contains(stLower, "extracting") ||
				strings.Contains(stLower, "verifying") ||
				strings.Contains(stLower, "download complete") ||
				strings.Contains(stLower, "pull complete") ||
				strings.Contains(stLower, "already exists")) {
			ls, ok := layers[ev.ID]
			if !ok {
				ls = &layerStat{}
				layers[ev.ID] = ls
			}
			if ev.ProgressDetail.Total > ls.total {
				ls.total = ev.ProgressDetail.Total
			}
			if ev.ProgressDetail.Current > ls.current {
				ls.current = ev.ProgressDetail.Current
			}
			if strings.Contains(stLower, "pull complete") || strings.Contains(stLower, "already exists") {
				ls.done = true
				if ls.total > 0 {
					ls.current = ls.total
				}
			}
		}

		var totalLayers, completedLayers int
		var downloadedBytes, totalBytes int64
		for _, l := range layers {
			totalLayers++
			if l.done {
				completedLayers++
			}
			downloadedBytes += l.current
			totalBytes += l.total
		}
		pct := 0
		if totalBytes > 0 {
			pct = int(downloadedBytes * 100 / totalBytes)
		} else if totalLayers > 0 {
			pct = completedLayers * 100 / totalLayers
		}
		if pct > 99 {
			pct = 99
		}
		emit(platform.PullProgress{
			Phase:           "pulling",
			StatusText:      ev.Status,
			TotalLayers:     totalLayers,
			CompletedLayers: completedLayers,
			DownloadedBytes: downloadedBytes,
			TotalBytes:      totalBytes,
			Percent:         pct,
		})
	}

	var ii imageInspect
	if err := a.getJSON(ctx, "/images/"+url.PathEscape(targetRef)+"/json", &ii); err != nil {
		return "", "", fmt.Errorf("读取新拉取镜像信息失败: %w", err)
	}
	for _, rd := range ii.RepoDigests {
		if _, d, ok := strings.Cut(rd, "@"); ok && strings.HasPrefix(d, "sha256:") {
			repoDigest = d
			break
		}
	}
	return ii.ID, repoDigest, nil
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
