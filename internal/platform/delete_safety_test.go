package platform

import "testing"

func TestIsProtectedHostPath(t *testing.T) {
	protectedCases := []string{
		"",
		"relative/path",
		"/",
		"/etc",
		"/etc/localtime",
		"/etc/timezone",
		"/var/run/docker.sock",
		"/run/docker.sock",
		"/proc/1",
		"/sys/fs/cgroup",
		"/var/lib/docker",
		"/var/lib/docker/volumes/foo/_data",
		"/var/packages/ContainerManager/etc",
		"/var/apps/docker",
		"/volume1",
		"/volume1/docker",
		"/volume2/homes",
		"/vol1",
		"/vol1/1000",
		"/vol1/1000/docker",
		"/vol1/@appcenter/dkstore",
		"/home",
		"/home/admin",
		"/root",
		"/root/.ssh",
		"/home/admin/.ssh/authorized_keys",
		"/opt",
		"/mnt",
	}
	for _, tc := range protectedCases {
		if !IsProtectedHostPath(tc) {
			t.Errorf("expected path %q to be protected, got false", tc)
		}
	}

	allowedCases := []string{
		"/opt/portainer",
		"/opt/docker/portainer/data",
		"/volume1/docker/immich",
		"/volume1/docker/immich/pgdata",
		"/vol1/1000/docker/immich",
		"/vol1/1000/Photos",
		"/mnt/sda1/docker/jellyfin/config",
		"/home/admin/docker/myapp",
		"/srv/myapp/data",
	}
	for _, tc := range allowedCases {
		if IsProtectedHostPath(tc) {
			t.Errorf("expected path %q to be allowed, got protected=true", tc)
		}
	}
}

func TestIsSubPathOrSame(t *testing.T) {
	if !IsSubPathOrSame("/vol1/1000/docker/immich", "/vol1/1000/docker/immich") {
		t.Fatal("expected exact match to return true")
	}
	if !IsSubPathOrSame("/vol1/1000/docker/immich/", "/vol1/1000/docker/immich/pgdata") {
		t.Fatal("expected child directory to return true")
	}
	if IsSubPathOrSame("/vol1/1000/docker/immich", "/vol1/1000/docker/immich-bak") {
		t.Fatal("expected sibling prefix directory to return false")
	}
}
