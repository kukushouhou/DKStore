package i18n

import "testing"

func TestAuthErrorCodesBilingual(t *testing.T) {
	codes := []string{"setup_required", "password_already_set", "password_too_short", "sse_unsupported", "updater_unavailable", "keygen_failed"}
	for _, c := range codes {
		for _, l := range []Lang{ZhCN, EnUS} {
			if APIErrorMessage(l, c, "") == "" {
				t.Errorf("missing %s message for %s", l, c)
			}
		}
	}
	hints := []string{"no_host_cookie", "invalid_host_cookie", "host_auth_unavailable", "host_session_invalid", "host_not_admin"}
	for _, h := range hints {
		for _, l := range []Lang{ZhCN, EnUS} {
			if HostAuthHint(l, h) == "" {
				t.Errorf("missing %s hint for %s", l, h)
			}
		}
	}
	if HostAuthHint(ZhCN, "unsupported_mode") != "" {
		t.Error("unsupported_mode should not produce a hint")
	}
}
