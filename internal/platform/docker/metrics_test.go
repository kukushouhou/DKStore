package docker

import (
	"testing"
)

func TestParseProcStatAndCalcCPU(t *testing.T) {
	s1 := []byte("cpu  1000 100 500 8000 400 0 0 0 50 0\ncpu0 500 50 250 4000 200 0 0 0 25 0\ncpu1 500 50 250 4000 200 0 0 0 25 0\n")
	s2 := []byte("cpu  1250 100 650 8450 550 0 0 0 50 0\ncpu0 625 50 325 4225 275 0 0 0 25 0\ncpu1 625 50 325 4225 275 0 0 0 25 0\n")

	t1, i1, c1, ok1 := parseProcStat(s1)
	t2, i2, c2, ok2 := parseProcStat(s2)
	if !ok1 || !ok2 {
		t.Fatalf("parseProcStat failed: ok1=%v ok2=%v", ok1, ok2)
	}
	if c1 != 2 || c2 != 2 {
		t.Fatalf("cores = (%d, %d), want 2", c1, c2)
	}
	// dt = (1250+100+650+8450+550) - (1000+100+500+8000+400) = 11000 - 10000 = 1000
	// di = (8450+550) - (8000+400) = 9000 - 8400 = 600
	// cpu = (1000 - 600) / 1000 = 40.0%
	pct := calcHostCPUPercent(t1, i1, t2, i2)
	if pct != 40.0 {
		t.Fatalf("cpu pct = %v, want 40.0", pct)
	}
}

func TestParseProcMeminfo(t *testing.T) {
	mem := []byte("MemTotal:       16384000 kB\nMemFree:         2000000 kB\nMemAvailable:   10240000 kB\nBuffers:          500000 kB\nCached:          7500000 kB\n")
	used, total := parseProcMeminfo(mem)
	if total != 16384000*1024 {
		t.Fatalf("total = %d, want %d", total, 16384000*1024)
	}
	wantUsed := int64(16384000-10240000) * 1024
	if used != wantUsed {
		t.Fatalf("used = %d, want %d", used, wantUsed)
	}
}

func TestParseDFDisks_SynologyFnOSOpenWrtAndOverlayFallback(t *testing.T) {
	// 1. 群晖多存储卷（自动过滤 /dev/md0 系统小分区与 @docker 子挂载）
	synoDF := []byte(`Filesystem              1024-blocks      Used Available Capacity Mounted on
/dev/md0                    2385528   1452100    814644      65% /
none                        8123456         0   8123456       0% /dev
/tmp                        8128000      1200   8126800       1% /tmp
/dev/mapper/cachedev_1   7690246912 210000000 7480246912      3% /volume2
/dev/mapper/cachedev_0   3845123456 912345678 2932777778     24% /volume1
/dev/mapper/cachedev_0   3845123456 912345678 2932777778     24% /volume1/@docker/btrfs
`)
	disks := parseDFDisks(synoDF)
	if len(disks) != 2 {
		t.Fatalf("synology disks len = %d, want 2: %+v", len(disks), disks)
	}
	if disks[0].Mount != "/volume1" || disks[1].Mount != "/volume2" {
		t.Fatalf("unexpected synology mount order: %+v", disks)
	}

	// 2. OpenWrt / iStoreOS（过滤 /rom，去重 /overlay 与 overlayfs:/overlay，去重同分区 /dev/sda3）
	owDF := []byte(`Filesystem           1024-blocks      Used Available Capacity Mounted on
/dev/root                  25600     25600         0     100% /rom
tmpfs                    1982400      2400   1980000       0% /tmp
/dev/loop0               2097152    345600   1751552      16% /overlay
overlayfs:/overlay       2097152    345600   1751552      16% /
/dev/sda3              246980000  72000000 174980000      29% /mnt/sda3
/dev/sda3              246980000  72000000 174980000      29% /opt
`)
	owDisks := parseDFDisks(owDF)
	if len(owDisks) != 2 {
		t.Fatalf("openwrt disks len = %d, want 2: %+v", len(owDisks), owDisks)
	}
	if owDisks[0].Mount != "/overlay" || owDisks[1].Mount != "/opt" {
		t.Fatalf("unexpected openwrt mounts: %+v", owDisks)
	}

	// 3. 容器化环境（仅存在容器根 overlay 时自动回退保留 /）
	containerDF := []byte(`Filesystem     1024-blocks     Used Available Capacity Mounted on
overlay          104857600 31457280  73400320      30% /
tmpfs                65536        0     65536       0% /dev
shm                  65536        0     65536       0% /dev/shm
`)
	cDisks := parseDFDisks(containerDF)
	if len(cDisks) != 1 || cDisks[0].Mount != "/" {
		t.Fatalf("container fallback disks = %+v, want [/]", cDisks)
	}
}
