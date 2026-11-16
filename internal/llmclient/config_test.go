package llmclient

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func setModelEnv(t *testing.T, base, model, key string) {
	t.Helper()
	t.Setenv("AI_BASE_URL", base)
	t.Setenv("AI_MODEL", model)
	t.Setenv("AI_API_KEY", key)
}

func TestLoadConfig(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		setModelEnv(t, "", "", "")
		cfg, err := LoadConfig()
		if err != nil || cfg.Enabled() {
			t.Fatalf("empty config enabled: %v, %v", cfg.Enabled(), err)
		}
		if _, err := NewClient(cfg); !errors.Is(err, ErrDisabled) {
			t.Fatalf("disabled constructor error=%v", err)
		}
		if _, err := (&Client{}).Complete(t.Context(), Request{}); !errors.Is(err, ErrDisabled) {
			t.Fatalf("disabled call error=%v", err)
		}
	})

	t.Run("valid addresses", func(t *testing.T) {
		for _, base := range []string{
			"https://example.test/v1", "https://example.test:8443/gateway/v1/",
			"http://localhost:8080/v1", "http://127.0.0.2/v1", "http://[::1]:8080/v1",
		} {
			setModelEnv(t, base, "test-model", "test-token")
			cfg, err := LoadConfig()
			if err != nil || !cfg.Enabled() || cfg.endpoint != strings.TrimSuffix(base, "/")+"/chat/completions" {
				t.Errorf("base %q: endpoint=%q, err=%v", base, cfg.endpoint, err)
			}
		}
	})
	t.Run("invalid or partial", func(t *testing.T) {
		for _, tc := range []struct{ base, model, key string }{
			{"https://example.test/v1", "", "test-token"},
			{"", "test-model", "test-token"},
			{"https://example.test/v1", "test-model", ""},
			{"http://example.test/v1", "test-model", "test-token"},
			{"http://localhost.example.test/v1", "test-model", "test-token"},
			{"http://[localhost]/v1", "test-model", "test-token"},
			{"https://example.test:/v1", "test-model", "test-token"},
			{"https://example.test:65536/v1", "test-model", "test-token"},
			{"https://user:test-token@example.test/v1", "test-model", "test-token"},
			{"https://example.test/v1?key=test-token", "test-model", "test-token"},
			{"https://example.test/v1?", "test-model", "test-token"},
			{"https://example.test/v1#test-token", "test-model", "test-token"},
			{"https://example.test/v1#", "test-model", "test-token"},
			{"https:/relative", "test-model", "test-token"},
			{"ftp://localhost/v1", "test-model", "test-token"},
			{"https://bad%space.test/v1", "test-model", "test-token"},
			{"https://example.test/v1", "test-model", "test-token\r\ninjected"},
		} {
			setModelEnv(t, tc.base, tc.model, tc.key)
			cfg, err := LoadConfig()
			if !errors.Is(err, ErrInvalidConfig) || cfg.Enabled() || strings.Contains(fmt.Sprint(err), "test-token") {
				t.Errorf("invalid config: enabled=%v, error classification correct=%v", cfg.Enabled(), errors.Is(err, ErrInvalidConfig))
			}
		}
	})
}
