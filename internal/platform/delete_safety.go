package platform

import (
	"path"
	"regexp"
	"strings"
)

var (
	reNASVolumeRoot     = regexp.MustCompile(`^/volume\d+(/docker|/homes)?$`)
	reFnOSVolumeRoot    = regexp.MustCompile(`^/vol\d+(/\d+(/docker)?)?$`)
	reFnOSSystemSubtree = regexp.MustCompile(`^/vol\d+/@(appcenter|appconf|appdata|apphome|appshare)(/|$)`)
	reUserHomeRoot      = regexp.MustCompile(`^/home/[^/]+$`)
	reSSHDir            = regexp.MustCompile(`^(/root|/home/[^/]+)/\.ssh(/|$)`)
	reWinDriveRoot      = regexp.MustCompile(`^[a-zA-Z]:/$`)
)

// NormalizeHostPath 归一化宿主机路径（转为正斜杠并清理冗余分隔符）。
func NormalizeHostPath(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsRune(s, 0) {
		return ""
	}
	s = strings.ReplaceAll(s, `\`, "/")
	if len(s) >= 2 && s[1] == ':' {
		prefix := s[:2]
		rest := s[2:]
		if !strings.HasPrefix(rest, "/") {
			return ""
		}
		cleaned := path.Clean(rest)
		return prefix + cleaned
	}
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	return path.Clean(s)
}

// IsProtectedHostPath 判断宿主机路径是否属于系统关键目录或存储根目录（严禁随容器/项目删除）。
func IsProtectedHostPath(raw string) bool {
	p := NormalizeHostPath(raw)
	if p == "" {
		return true
	}
	if p == "/" || reWinDriveRoot.MatchString(p) {
		return true
	}
	// Windows 开发/测试环境仅放行显式深层路径，保护系统盘常见顶级目录
	if len(p) >= 3 && p[1] == ':' {
		lower := strings.ToLower(p[2:])
		for _, winSys := range []string{"/windows", "/program files", "/program files (x86)", "/programdata", "/users"} {
			if lower == winSys {
				return true
			}
			if winSys != "/users" && strings.HasPrefix(lower, winSys+"/") {
				return true
			}
		}
		return false
	}

	topLevelProtected := map[string]bool{
		"/":      true,
		"/bin":   true,
		"/boot":  true,
		"/dev":   true,
		"/etc":   true,
		"/home":  true,
		"/lib":   true,
		"/lib32": true,
		"/lib64": true,
		"/media": true,
		"/mnt":   true,
		"/opt":   true,
		"/proc":  true,
		"/root":  true,
		"/run":   true,
		"/sbin":  true,
		"/srv":   true,
		"/sys":   true,
		"/tmp":   true,
		"/usr":   true,
		"/var":   true,
	}
	if topLevelProtected[p] {
		return true
	}

	// 整棵子树均受保护的系统路径
	protectedTrees := []string{
		"/bin/",
		"/boot/",
		"/dev/",
		"/etc/",
		"/lib/",
		"/lib32/",
		"/lib64/",
		"/proc/",
		"/run/",
		"/sbin/",
		"/sys/",
		"/usr/",
		"/var/run/",
		"/var/lock/",
		"/var/lib/docker/",
		"/var/packages/",
		"/var/apps/",
	}
	if p == "/var/lib/docker" || p == "/var/packages" || p == "/var/apps" {
		return true
	}
	for _, prefix := range protectedTrees {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}

	if reNASVolumeRoot.MatchString(p) || reFnOSVolumeRoot.MatchString(p) || reFnOSSystemSubtree.MatchString(p) {
		return true
	}
	if reUserHomeRoot.MatchString(p) || reSSHDir.MatchString(p) {
		return true
	}
	return false
}

// IsSubPathOrSame 判断 child 是否等于 parent 或位于 parent 目录内部。
func IsSubPathOrSame(parent, child string) bool {
	p := NormalizeHostPath(parent)
	c := NormalizeHostPath(child)
	if p == "" || c == "" {
		return false
	}
	if p == c {
		return true
	}
	return strings.HasPrefix(c, strings.TrimRight(p, "/")+"/")
}
