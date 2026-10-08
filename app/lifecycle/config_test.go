package lifecycle_test

import (
	"strings"
	"testing"
	"time"

	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
	"github.com/standards-lab/go-core/config"
)

func TestConfigFinalize(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("APP_SHUTDOWN_TIMEOUT", "")
		var cfg lifecycle.Config
		if err := cfg.Finalize("app"); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if got := cfg.ShutdownTimeout.Duration(); got != 10*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 10s", got)
		}
	})

	t.Run("keeps a set value", func(t *testing.T) {
		t.Setenv("APP_SHUTDOWN_TIMEOUT", "")
		cfg := lifecycle.Config{ShutdownTimeout: config.Duration(time.Second)}
		if err := cfg.Finalize("app"); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if got := cfg.ShutdownTimeout.Duration(); got != time.Second {
			t.Errorf("ShutdownTimeout = %v, want 1s", got)
		}
	})

	t.Run("environment overrides", func(t *testing.T) {
		t.Setenv("APP_SHUTDOWN_TIMEOUT", "3s")
		cfg := lifecycle.Config{ShutdownTimeout: config.Duration(time.Second)}
		if err := cfg.Finalize("app"); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if got := cfg.ShutdownTimeout.Duration(); got != 3*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 3s", got)
		}
	})

	t.Run("invalid override names the variable", func(t *testing.T) {
		t.Setenv("APP_SHUTDOWN_TIMEOUT", "soon")
		var cfg lifecycle.Config
		err := cfg.Finalize("app")
		if err == nil || !strings.Contains(err.Error(), "APP_SHUTDOWN_TIMEOUT") {
			t.Errorf("Finalize = %v, want an error naming APP_SHUTDOWN_TIMEOUT", err)
		}
	})

	t.Run("rejects a timeout that is not positive", func(t *testing.T) {
		t.Setenv("APP_SHUTDOWN_TIMEOUT", "-1s")
		var cfg lifecycle.Config
		if err := cfg.Finalize("app"); err == nil {
			t.Error("Finalize = nil, want an error for a negative timeout")
		}
	})
}
