package platform

import (
	"sort"
	"strconv"
	"strings"
)

// ContainerMetaKey 生成单个容器在 local_meta 表中的稳定标识键。
// 独立容器直接复用 appKey；多容器项目下的服务/容器使用 "<appKey>/svc/<service>" 或 "<appKey>/ctr/<name>"。
func ContainerMetaKey(appKey, serviceName, containerName string) string {
	appKey = strings.TrimSpace(appKey)
	if appKey == "" {
		return ""
	}
	if strings.Contains(appKey, "/container/") {
		return appKey
	}
	svc := strings.TrimSpace(serviceName)
	if svc != "" && svc != "main" {
		return appKey + "/svc/" + svc
	}
	cname := strings.TrimPrefix(strings.TrimSpace(containerName), "/")
	if cname != "" {
		return appKey + "/ctr/" + cname
	}
	return appKey
}

var nonWebPortSet = map[int]bool{
	21: true, 22: true, 25: true, 53: true, 110: true, 139: true, 143: true,
	389: true, 445: true, 465: true, 554: true, 587: true, 636: true,
	993: true, 995: true, 1433: true, 1521: true, 1883: true, 1935: true,
	2181: true, 3306: true, 3389: true, 3478: true, 5432: true, 5672: true,
	6379: true, 6881: true, 8554: true, 8883: true, 9042: true, 11211: true,
	22000: true, 25565: true, 27017: true, 51413: true,
}

var preferredWebPortSet = map[int]bool{
	80: true, 81: true, 443: true, 2283: true, 3000: true, 3001: true,
	4000: true, 5000: true, 5001: true, 5173: true, 5244: true, 8000: true,
	8006: true, 8080: true, 8081: true, 8085: true, 8090: true, 8096: true,
	8123: true, 8200: true, 8384: true, 8443: true, 8888: true, 9000: true,
	9090: true, 9091: true, 9443: true, 15672: true, 32400: true,
}

func isLoopbackIP(ip string) bool {
	s := strings.Trim(strings.TrimSpace(ip), "[]")
	return s == "127.0.0.1" || s == "::1" || strings.EqualFold(s, "localhost")
}

func normalizeBindHostIP(ip string) string {
	s := strings.Trim(strings.TrimSpace(ip), "[]")
	switch s {
	case "", "0.0.0.0", "::", "*":
		return ""
	default:
		return s
	}
}

func webPortRank(p PortBinding) (loopback int, category int, num int) {
	if isLoopbackIP(p.HostIP) {
		loopback = 1
	}
	hp, _ := strconv.Atoi(p.HostPort)
	cp, _ := strconv.Atoi(p.ContainerPort)
	num = hp
	if nonWebPortSet[cp] || nonWebPortSet[hp] {
		return loopback, 30, num
	}
	if preferredWebPortSet[cp] || preferredWebPortSet[hp] {
		return loopback, 10, num
	}
	return loopback, 20, num
}

// FilterAndSortWebPorts 过滤出对外映射的有效 TCP 宿主机端口，去重并按 Web 端口优先级排序。
func FilterAndSortWebPorts(in []PortBinding) []PortBinding {
	if len(in) == 0 {
		return nil
	}
	byHostPort := make(map[string]PortBinding, len(in))
	for _, raw := range in {
		proto := strings.ToLower(strings.TrimSpace(raw.Protocol))
		if proto != "" && proto != "tcp" {
			continue
		}
		hpStr := strings.TrimSpace(raw.HostPort)
		hp, err := strconv.Atoi(hpStr)
		if err != nil || hp <= 0 || hp > 65535 {
			continue
		}
		cpStr := strings.TrimSpace(raw.ContainerPort)
		norm := PortBinding{
			HostIP:        normalizeBindHostIP(raw.HostIP),
			HostPort:      strconv.Itoa(hp),
			ContainerPort: cpStr,
			Protocol:      "tcp",
		}
		prev, exists := byHostPort[norm.HostPort]
		if !exists {
			byHostPort[norm.HostPort] = norm
			continue
		}
		// 若已有记录仅绑定回环地址而新记录可外部访问，或新记录具备明确局域网 IP，则替换
		if isLoopbackIP(prev.HostIP) && !isLoopbackIP(norm.HostIP) {
			byHostPort[norm.HostPort] = norm
		} else if prev.HostIP == "" && norm.HostIP != "" && !isLoopbackIP(norm.HostIP) {
			byHostPort[norm.HostPort] = norm
		}
	}
	if len(byHostPort) == 0 {
		return nil
	}
	out := make([]PortBinding, 0, len(byHostPort))
	for _, v := range byHostPort {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		l1, c1, n1 := webPortRank(out[i])
		l2, c2, n2 := webPortRank(out[j])
		if l1 != l2 {
			return l1 < l2
		}
		if c1 != c2 {
			return c1 < c2
		}
		return n1 < n2
	})
	return out
}
