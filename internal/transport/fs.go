package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kukushouhou/DKStore/internal/i18n"
)

// SafeDirNameRe 约束新建目录与项目子目录名称：仅允许英文字母、数字、点、连字符与下划线，且以字母或数字开头。
var SafeDirNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// IsSafeDirSegment 校验单个目录名片段是否安全且兼容各平台 Docker Compose 路径解析。
func IsSafeDirSegment(name string) bool {
	s := strings.TrimSpace(name)
	if s == "" || s == "." || s == ".." {
		return false
	}
	return SafeDirNameRe.MatchString(s)
}

// FileAccessState 描述目标配置文件及其所在目录在目标主机上的存在性与读写权限。
type FileAccessState struct {
	Exists   bool `json:"exists"`
	Readable bool `json:"readable"`
	Writable bool `json:"writable"`
}

// DirListing 描述目标主机上单个目录的子文件夹列表及可写状态。
type DirListing struct {
	Path     string   `json:"path"`
	Dirs     []string `json:"dirs"`
	Writable bool     `json:"writable"`
	HomeDir  string   `json:"homeDir,omitempty"`
}

// HostFSTransport 扩展 Transport，提供宿主机配置文件权限探测、目录浏览与安全新建目录能力。
type HostFSTransport interface {
	Transport
	ProbeFileAccess(ctx context.Context, filePath string) (FileAccessState, error)
	ListDirectories(ctx context.Context, dirPath string) (*DirListing, error)
	CreateDirectory(ctx context.Context, dirPath string) error
}

func normalizePosixDir(raw string) string {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if s == "" {
		return "/"
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	cleaned := path.Clean(s)
	if cleaned == "" || cleaned == "." {
		return "/"
	}
	return cleaned
}

func isPseudoReadOnlyDir(p string) bool {
	clean := normalizePosixDir(p)
	return clean == "/proc" || strings.HasPrefix(clean, "/proc/") ||
		clean == "/sys" || strings.HasPrefix(clean, "/sys/") ||
		clean == "/dev" || strings.HasPrefix(clean, "/dev/")
}

func isLocalDirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".dkstore-perm-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// ProbeFileAccess 探测本机目标配置文件是否存在、可读、可写（文件不存在时探测最近已存在父目录是否可创建）。
func (l *LocalTransport) ProbeFileAccess(_ context.Context, filePath string) (FileAccessState, error) {
	p := strings.TrimSpace(filePath)
	if p == "" || strings.ContainsRune(p, 0) {
		return FileAccessState{}, i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	if isPseudoReadOnlyDir(p) {
		return FileAccessState{}, nil
	}
	st, err := os.Stat(p)
	if err == nil {
		if st.IsDir() {
			return FileAccessState{Exists: false, Readable: false, Writable: false}, nil
		}
		readable := false
		if rf, rErr := os.Open(p); rErr == nil {
			readable = true
			_ = rf.Close()
		}
		writable := false
		if wf, wErr := os.OpenFile(p, os.O_WRONLY, 0); wErr == nil {
			_ = wf.Close()
			writable = isLocalDirWritable(filepath.Dir(p))
		}
		return FileAccessState{Exists: true, Readable: readable, Writable: writable}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return FileAccessState{}, nil
	}
	cur := filepath.Dir(p)
	for cur != "" && cur != "." {
		if info, sErr := os.Stat(cur); sErr == nil {
			if !info.IsDir() {
				return FileAccessState{}, nil
			}
			return FileAccessState{Exists: false, Readable: false, Writable: isLocalDirWritable(cur)}, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return FileAccessState{}, nil
}

// ListDirectories 列出本机指定路径下的可见子文件夹。
func (l *LocalTransport) ListDirectories(_ context.Context, dirPath string) (*DirListing, error) {
	clean := strings.TrimSpace(dirPath)
	if clean == "" {
		clean = "/"
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name)
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			if st, sErr := os.Stat(filepath.Join(clean, name)); sErr == nil && st.IsDir() {
				dirs = append(dirs, name)
			}
		}
	}
	sort.Strings(dirs)
	homeDir, _ := os.UserHomeDir()
	return &DirListing{
		Path:     filepath.ToSlash(clean),
		Dirs:     dirs,
		Writable: !isPseudoReadOnlyDir(clean) && isLocalDirWritable(clean),
		HomeDir:  filepath.ToSlash(homeDir),
	}, nil
}

// CreateDirectory 在本机创建新目录（叶节点须符合安全目录命名规范）。
func (l *LocalTransport) CreateDirectory(_ context.Context, dirPath string) error {
	clean := strings.TrimSpace(dirPath)
	if clean == "" || strings.ContainsRune(clean, 0) {
		return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	base := filepath.Base(clean)
	if !IsSafeDirSegment(base) {
		return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	return os.MkdirAll(clean, 0o755)
}

func (t *SSHTransport) runHostProbeWithSudoFallback(ctx context.Context, script string, escalate func(out string, err error) bool) (string, error) {
	cmd := "sh -c " + shellQuote(script)
	if t.mode == ModeSudoStdio {
		b, err := t.runSession(ctx, "sudo -n "+cmd, nil, 256*1024, false)
		return string(b), err
	}
	if t.mode == ModeSudoPassStdio && t.cfg.Secret != "" {
		b, err := t.runSession(ctx, "sudo -k -S -p '' "+cmd, sudoStdin(t.cfg.Secret, nil), 256*1024, false)
		return string(b), err
	}
	b, err := t.runSession(ctx, cmd, nil, 256*1024, false)
	out := string(b)
	if !escalate(out, err) || ctx.Err() != nil || errors.Is(err, ErrTransportClosed) {
		return out, err
	}
	if sb, sErr := t.runSession(ctx, "sudo -n "+cmd, nil, 256*1024, false); sErr == nil {
		return string(sb), nil
	}
	if t.cfg.AuthType != "key" && t.cfg.Secret != "" {
		if sb, sErr := t.runSession(ctx, "sudo -k -S -p '' "+cmd, sudoStdin(t.cfg.Secret, nil), 256*1024, false); sErr == nil {
			return string(sb), nil
		}
	}
	return out, err
}

// ProbeFileAccess 探测远端主机目标配置文件的存在性与读写权限（若普通权限受限则自动探测 sudo 提权后是否可读写）。
func (t *SSHTransport) ProbeFileAccess(ctx context.Context, filePath string) (FileAccessState, error) {
	p := normalizePosixDir(filePath)
	if strings.TrimSpace(filePath) == "" || strings.ContainsRune(filePath, 0) || p == "/" {
		return FileAccessState{}, i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	if isPseudoReadOnlyDir(p) {
		return FileAccessState{}, nil
	}
	if err := t.ensureClient(ctx); err != nil {
		return FileAccessState{}, err
	}

	script := fmt.Sprintf(`p=%s
if [ -d "$p" ]; then
  printf '0 0 0\n'
elif [ -e "$p" ]; then
  r=0; w=0
  [ -r "$p" ] && r=1
  dp=$(dirname -- "$p")
  [ -w "$p" ] && [ -w "$dp" ] && w=1
  printf '1 %%d %%d\n' "$r" "$w"
else
  d=$(dirname -- "$p")
  while [ ! -e "$d" ] && [ "$d" != "/" ] && [ "$d" != "." ]; do
    d=$(dirname -- "$d")
  done
  w=0
  [ -d "$d" ] && [ -w "$d" ] && w=1
  printf '0 0 %%d\n' "$w"
fi`, shellQuote(p))

	parseState := func(raw string) (FileAccessState, bool) {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) < 3 {
			return FileAccessState{}, false
		}
		return FileAccessState{
			Exists:   fields[0] == "1",
			Readable: fields[1] == "1",
			Writable: fields[2] == "1",
		}, true
	}

	out, err := t.runHostProbeWithSudoFallback(ctx, script, func(s string, e error) bool {
		if e != nil {
			return true
		}
		st, ok := parseState(s)
		if !ok {
			return true
		}
		// 若普通权限不可写或不可读，尝试看 sudo 提权后是否可读写（与 WriteFileAtomic/ReadFile 行为严格一致）
		return !st.Writable || (st.Exists && !st.Readable)
	})
	if err != nil {
		return FileAccessState{}, err
	}
	st, _ := parseState(out)
	return st, nil
}

// ListDirectories 列出远端主机指定目录下的可见子目录、可写状态及当前登录账号主目录。
func (t *SSHTransport) ListDirectories(ctx context.Context, dirPath string) (*DirListing, error) {
	clean := normalizePosixDir(dirPath)
	if strings.ContainsRune(dirPath, 0) {
		return nil, i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	if err := t.ensureClient(ctx); err != nil {
		return nil, err
	}

	script := fmt.Sprintf(`orig_home="$HOME"
cd -- %s || exit 1
cur=$(pwd)
w=0
[ -w . ] && w=1
printf 'META\t%%s\t%%d\t%%s\n' "$cur" "$w" "$orig_home"
for entry in *; do
  [ -e "$entry" ] || continue
  if [ -d "$entry" ]; then
    printf 'DIR\t%%s\n' "$entry"
  fi
done`, shellQuote(clean))

	parseListing := func(raw string) (*DirListing, bool) {
		lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
		res := &DirListing{Path: clean, Dirs: []string{}}
		gotMeta := false
		for _, line := range lines {
			if strings.HasPrefix(line, "META\t") {
				parts := strings.SplitN(line, "\t", 4)
				if len(parts) >= 3 {
					gotMeta = true
					if strings.TrimSpace(parts[1]) != "" {
						res.Path = normalizePosixDir(parts[1])
					}
					res.Writable = parts[2] == "1" && !isPseudoReadOnlyDir(res.Path)
					if len(parts) == 4 {
						res.HomeDir = strings.TrimSpace(parts[3])
					}
				}
			} else if strings.HasPrefix(line, "DIR\t") {
				name := strings.TrimPrefix(line, "DIR\t")
				if name != "" && name != "." && name != ".." && !strings.HasPrefix(name, ".") {
					res.Dirs = append(res.Dirs, name)
				}
			}
		}
		sort.Strings(res.Dirs)
		return res, gotMeta
	}

	// 先读取非 sudo 下的真实用户 HOME 目录，以便即使走 sudo 后快捷跳转仍指向用户自己的家目录
	var sshUserHome string
	if hb, hErr := t.runSession(ctx, `sh -c 'printf "%s" "$HOME"'`, nil, 4096, false); hErr == nil {
		sshUserHome = strings.TrimSpace(string(hb))
	}

	out, err := t.runHostProbeWithSudoFallback(ctx, script, func(s string, e error) bool {
		if e != nil {
			return true
		}
		parsed, ok := parseListing(s)
		return !ok || !parsed.Writable
	})
	if err != nil {
		return nil, err
	}
	res, ok := parseListing(out)
	if !ok {
		return nil, fmt.Errorf("unexpected directory listing output")
	}
	if sshUserHome != "" {
		res.HomeDir = normalizePosixDir(sshUserHome)
	}
	return res, nil
}

// CreateDirectory 在远端主机上安全创建新文件夹（支持自动 sudo 提权）。
func (t *SSHTransport) CreateDirectory(ctx context.Context, dirPath string) error {
	clean := normalizePosixDir(dirPath)
	if strings.ContainsRune(dirPath, 0) || clean == "/" || isPseudoReadOnlyDir(clean) {
		return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	base := path.Base(clean)
	if !IsSafeDirSegment(base) {
		return i18n.NewEngineError(i18n.MsgTransportInvalidPath)
	}
	if err := t.ensureClient(ctx); err != nil {
		return err
	}
	cmd := "mkdir -p -- " + shellQuote(clean)
	out, err := t.runPrivilegedCmd(ctx, "sh -c "+shellQuote(cmd), nil)
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w (%s)", err, msg)
		}
		return err
	}
	return nil
}
