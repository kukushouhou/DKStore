package docker

import (
	"bufio"
	"bytes"
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/platform"
	"github.com/kukushouhou/DKStore/internal/transport"
)

type hostCPUSample struct {
	total uint64
	idle  uint64
	cores int
	at    time.Time
}

const hostCPUSampleMaxAge = 30 * time.Second

// CollectHostMetrics 采集宿主机真实 CPU 占用、物理内存占用与有效数据磁盘剩余空间。
func (a *Adapter) CollectHostMetrics(ctx context.Context) (*platform.HostRuntimeStat, error) {
	// 同时探测 Docker Engine 存活并提供核心数与总内存兜底
	var info dockerInfoResp
	if err := a.getJSON(ctx, "/info", &info); err != nil {
		return nil, i18n.NewEngineError(i18n.MsgDockerProbeFailed, i18n.DetailOf(err))
	}

	now := time.Now()
	a.statsMu.Lock()
	prev := a.prevHostCPU
	needDelta := prev == nil || now.Sub(prev.at) > hostCPUSampleMaxAge
	a.statsMu.Unlock()

	var raw *transport.HostMetricsRaw
	if hmTr, ok := a.tr.(transport.HostMetricsTransport); ok {
		r, err := hmTr.ReadHostMetricsRaw(ctx, needDelta)
		if err == nil {
			raw = r
		}
	}
	if raw == nil {
		stat1, _ := a.tr.ReadFile(ctx, "/proc/stat")
		var stat2 []byte
		if needDelta && len(stat1) > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(180 * time.Millisecond):
			}
			stat2, _ = a.tr.ReadFile(ctx, "/proc/stat")
		}
		mem, _ := a.tr.ReadFile(ctx, "/proc/meminfo")
		raw = &transport.HostMetricsRaw{
			ProcStat1:   stat1,
			ProcStat2:   stat2,
			ProcMeminfo: mem,
		}
	}

	var cpuPct float64
	var cores int
	if len(raw.ProcStat2) > 0 {
		t1, i1, c1, ok1 := parseProcStat(raw.ProcStat1)
		t2, i2, c2, ok2 := parseProcStat(raw.ProcStat2)
		if ok1 && ok2 {
			cpuPct = calcHostCPUPercent(t1, i1, t2, i2)
			cores = c2
			if cores == 0 {
				cores = c1
			}
			a.statsMu.Lock()
			a.prevHostCPU = &hostCPUSample{total: t2, idle: i2, cores: cores, at: time.Now()}
			a.statsMu.Unlock()
		}
	} else if len(raw.ProcStat1) > 0 {
		tCur, iCur, cCur, okCur := parseProcStat(raw.ProcStat1)
		if okCur {
			cores = cCur
			a.statsMu.Lock()
			if a.prevHostCPU != nil && now.Sub(a.prevHostCPU.at) <= hostCPUSampleMaxAge && tCur > a.prevHostCPU.total {
				cpuPct = calcHostCPUPercent(a.prevHostCPU.total, a.prevHostCPU.idle, tCur, iCur)
			}
			a.prevHostCPU = &hostCPUSample{total: tCur, idle: iCur, cores: cores, at: now}
			a.statsMu.Unlock()
		}
	}
	if cores <= 0 && info.NCPU > 0 {
		cores = info.NCPU
	}

	memUsed, memTotal := parseProcMeminfo(raw.ProcMeminfo)
	if memTotal <= 0 && info.MemTotal > 0 {
		memTotal = info.MemTotal
	}

	disks := parseDFDisks(raw.DiskDF)

	return &platform.HostRuntimeStat{
		HostID:        a.hostID,
		Online:        true,
		CPUPercent:    cpuPct,
		CPUCores:      cores,
		MemUsedBytes:  memUsed,
		MemTotalBytes: memTotal,
		Disks:         disks,
		SampledAt:     time.Now().Unix(),
	}, nil
}

func parseProcStat(b []byte) (total, idle uint64, cores int, ok bool) {
	if len(b) == 0 {
		return 0, 0, 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "cpu ") || strings.HasPrefix(line, "cpu\t") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			// fields[1..8]: user nice system idle iowait irq softirq steal
			// guest 与 guest_nice 已被内核计入 user / nice，不重复累加
			limit := len(fields)
			if limit > 9 {
				limit = 9
			}
			var vals [9]uint64
			for idx := 1; idx < limit; idx++ {
				v, err := strconv.ParseUint(fields[idx], 10, 64)
				if err == nil {
					vals[idx] = v
					total += v
				}
			}
			idle = vals[4] + vals[5]
			ok = total > 0
			continue
		}
		if strings.HasPrefix(line, "cpu") && len(line) > 3 && line[3] >= '0' && line[3] <= '9' {
			cores++
		}
	}
	return total, idle, cores, ok
}

func calcHostCPUPercent(t1, i1, t2, i2 uint64) float64 {
	if t2 <= t1 {
		return 0
	}
	dt := float64(t2 - t1)
	var di float64
	if i2 > i1 {
		di = float64(i2 - i1)
	}
	pct := (dt - di) / dt * 100.0
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return math.Round(pct*10) / 10
}

func parseProcMeminfo(b []byte) (usedBytes, totalBytes int64) {
	if len(b) == 0 {
		return 0, 0
	}
	var (
		memTotal     int64
		memAvail     int64
		hasAvail     bool
		memFree      int64
		buffers      int64
		cached       int64
		sReclaimable int64
		shmem        int64
	)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		valKB, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(k) {
		case "MemTotal":
			memTotal = valKB
		case "MemAvailable":
			memAvail = valKB
			hasAvail = true
		case "MemFree":
			memFree = valKB
		case "Buffers":
			buffers = valKB
		case "Cached":
			cached = valKB
		case "SReclaimable":
			sReclaimable = valKB
		case "Shmem":
			shmem = valKB
		}
	}
	if memTotal <= 0 {
		return 0, 0
	}
	avail := memAvail
	if !hasAvail {
		avail = memFree + buffers + cached + sReclaimable - shmem
	}
	if avail < 0 {
		avail = 0
	}
	if avail > memTotal {
		avail = memTotal
	}
	used := memTotal - avail
	return used * 1024, memTotal * 1024
}

var (
	reDFLine    = regexp.MustCompile(`^(\S+(?:\s+\S+)*?)\s+(\d+)\s+(\d+)\s+(\d+)\s+\S+%\s+(/.*)$`)
	reSynVolume = regexp.MustCompile(`^/volume(\d+)$`)
	reFnVolume  = regexp.MustCompile(`^/vol(\d+)$`)
)

func parseDFDisks(b []byte) []platform.DiskStat {
	if len(b) == 0 {
		return nil
	}
	type rawEntry struct {
		fs      string
		totalKB int64
		availKB int64
		mount   string
	}

	var rawEntries []rawEntry
	var pendingFS string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "filesystem ") || strings.HasPrefix(lower, "filesystem\t") {
			pendingFS = ""
			continue
		}
		if pendingFS != "" {
			line = pendingFS + " " + line
			pendingFS = ""
		}
		if !strings.ContainsAny(line, " \t") {
			pendingFS = line
			continue
		}
		m := reDFLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		totalKB, err1 := strconv.ParseInt(m[2], 10, 64)
		availKB, err2 := strconv.ParseInt(m[4], 10, 64)
		if err1 != nil || err2 != nil || totalKB <= 0 {
			continue
		}
		rawEntries = append(rawEntries, rawEntry{
			fs:      strings.TrimSpace(m[1]),
			totalKB: totalKB,
			availKB: availKB,
			mount:   strings.TrimSpace(m[5]),
		})
	}

	var primary []rawEntry
	var overlayRoot *rawEntry

	for _, e := range rawEntries {
		if shouldSkipMountPath(e.mount) {
			continue
		}
		fsLower := strings.ToLower(e.fs)
		if shouldSkipPseudoFS(fsLower) {
			continue
		}
		// 容器环境兜底：若整个环境只有容器根 overlay，暂存备用；存在真实块设备挂载时跳过纯 overlay
		if fsLower == "overlay" && e.mount == "/" {
			cp := e
			overlayRoot = &cp
			continue
		}
		if fsLower == "overlay" || strings.HasPrefix(fsLower, "fuse.mergerfs") || fsLower == "shfs" {
			continue
		}
		// 过滤小于 8 MiB 的微型固件分区
		if e.totalKB < 8192 {
			continue
		}
		primary = append(primary, e)
	}

	if len(primary) == 0 && overlayRoot != nil {
		primary = append(primary, *overlayRoot)
	}
	if len(primary) == 0 {
		return nil
	}

	hasSynologyVolume := false
	hasOpenWrtOverlay := false
	for _, e := range primary {
		if reSynVolume.MatchString(e.mount) {
			hasSynologyVolume = true
		}
		if e.mount == "/overlay" {
			hasOpenWrtOverlay = true
		}
	}

	// 平台级去噪：
	// 1. 群晖存在 /volumeN 存储空间时，过滤约 2.3GB 的 /dev/md0 系统固件根分区及 /volumeN/@... 内部子挂载
	// 2. OpenWrt 存在 /overlay 时，过滤同源的 overlayfs:/overlay 根挂载
	filtered := make([]rawEntry, 0, len(primary))
	for _, e := range primary {
		if hasSynologyVolume {
			if e.mount == "/" && (e.fs == "/dev/md0" || e.fs == "/dev/root" || e.totalKB < 8*1024*1024) {
				continue
			}
			if strings.HasPrefix(e.mount, "/volume") && strings.Contains(e.mount, "/@") {
				continue
			}
		}
		if hasOpenWrtOverlay && e.mount == "/" && strings.Contains(strings.ToLower(e.fs), "overlay") {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 0 {
		filtered = primary
	}

	// 同一底层块设备（如 Btrfs 子卷或 bind mount）去重，保留优先级最高的主挂载点
	byDevice := make(map[string]rawEntry)
	var uniqueOrder []string
	for _, e := range filtered {
		devKey := e.fs
		if !strings.HasPrefix(devKey, "/dev/") {
			devKey = e.fs + "::" + e.mount
		}
		prev, exists := byDevice[devKey]
		if !exists {
			byDevice[devKey] = e
			uniqueOrder = append(uniqueOrder, devKey)
			continue
		}
		if mountPriority(e.mount) < mountPriority(prev.mount) {
			byDevice[devKey] = e
		}
	}

	out := make([]platform.DiskStat, 0, len(uniqueOrder))
	for _, k := range uniqueOrder {
		e := byDevice[k]
		totalB := e.totalKB * 1024
		freeB := e.availKB * 1024
		if freeB < 0 {
			freeB = 0
		}
		if freeB > totalB {
			freeB = totalB
		}
		usedB := totalB - freeB
		out = append(out, platform.DiskStat{
			Mount:      e.mount,
			Filesystem: e.fs,
			TotalBytes: totalB,
			FreeBytes:  freeB,
			UsedBytes:  usedB,
		})
	}

	sortDiskStats(out)
	return out
}

func shouldSkipMountPath(mount string) bool {
	if mount == "" || !strings.HasPrefix(mount, "/") {
		return true
	}
	switch mount {
	case "/dev", "/proc", "/sys", "/run", "/tmp", "/var/tmp", "/boot", "/boot/efi", "/rom",
		"/etc/resolv.conf", "/etc/hostname", "/etc/hosts":
		return true
	}
	for _, pfx := range []string{"/dev/", "/proc/", "/sys/", "/run/", "/boot/", "/snap/", "/var/snap/", "/var/packages/", "/var/apps/"} {
		if strings.HasPrefix(mount, pfx) {
			return true
		}
	}
	for _, sub := range []string{
		"/docker/overlay2/", "/docker/containers/", "/docker/btrfs/", "/docker/zfs/",
		"/containers/storage/", "/kubelet/", "/@docker/", "/@appcenter/", "/@appdata/", "/@apphome/", "/@appshare/", "/@appconf/",
	} {
		if strings.Contains(mount, sub) {
			return true
		}
	}
	return false
}

func shouldSkipPseudoFS(fsLower string) bool {
	switch fsLower {
	case "tmpfs", "devtmpfs", "proc", "sysfs", "cgroup", "cgroup2", "mqueue", "hugetlbfs",
		"debugfs", "tracefs", "securityfs", "pstore", "autofs", "fusectl", "configfs",
		"efivarfs", "binfmt_misc", "nsfs", "shm", "none", "udev", "squashfs", "iso9660",
		"udf", "ramfs", "rootfs", "rpc_pipefs", "nfsd":
		return true
	}
	for _, pfx := range []string{"tmpfs", "devtmpfs", "cgroup", "nsfs", "squashfs", "fuse.lxcfs", "fuse.gvfsd"} {
		if strings.HasPrefix(fsLower, pfx) {
			return true
		}
	}
	if strings.Contains(fsLower, ":/") && !strings.HasPrefix(fsLower, "overlayfs:/overlay") {
		// 跳过远程 NFS 挂载 (host:/export)
		return true
	}
	if strings.HasPrefix(fsLower, "//") {
		// 跳过远程 SMB/CIFS 挂载 (//server/share)
		return true
	}
	return false
}

func mountPriority(mount string) int {
	if reSynVolume.MatchString(mount) {
		return 10 + len(mount)
	}
	if reFnVolume.MatchString(mount) {
		return 20 + len(mount)
	}
	if mount == "/" {
		return 30
	}
	if mount == "/overlay" {
		return 35
	}
	return 100 + len(mount)
}

func sortDiskStats(disks []platform.DiskStat) {
	sort.Slice(disks, func(i, j int) bool {
		ri, ni := diskSortRank(disks[i].Mount)
		rj, nj := diskSortRank(disks[j].Mount)
		if ri != rj {
			return ri < rj
		}
		if ni != nj {
			return ni < nj
		}
		return disks[i].Mount < disks[j].Mount
	})
}

func diskSortRank(mount string) (rank int, num int) {
	if m := reSynVolume.FindStringSubmatch(mount); m != nil {
		n, _ := strconv.Atoi(m[1])
		return 1, n
	}
	if m := reFnVolume.FindStringSubmatch(mount); m != nil {
		n, _ := strconv.Atoi(m[1])
		return 2, n
	}
	if mount == "/" {
		return 3, 0
	}
	if mount == "/overlay" {
		return 4, 0
	}
	return 5, 0
}
