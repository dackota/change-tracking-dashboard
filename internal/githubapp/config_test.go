package githubapp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/githubapp"
)

// --- Config behavior: disabled when no env vars are set ---

func TestFromEnv_Disabled_WhenNoVarsSet(t *testing.T) {
	t.Setenv(githubapp.EnvAppID, "")
	t.Setenv(githubapp.EnvInstallationID, "")
	t.Setenv(githubapp.EnvPrivateKeyFile, "")

	_, enabled, err := githubapp.FromEnv()
	if err != nil {
		t.Fatalf("unexpected error when no vars set: %v", err)
	}
	if enabled {
		t.Error("expected enabled=false when no env vars are set")
	}
}

// --- Config behavior: returns config when all three vars are set ---

func TestFromEnv_ReturnsConfig_WhenAllVarsSet(t *testing.T) {
	privateKeyPEM := generateTestKey(t)

	keyFile := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyFile, privateKeyPEM, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	t.Setenv(githubapp.EnvAppID, "42")
	t.Setenv(githubapp.EnvInstallationID, "7")
	t.Setenv(githubapp.EnvPrivateKeyFile, keyFile)

	cfg, enabled, err := githubapp.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv error: %v", err)
	}
	if !enabled {
		t.Fatal("expected enabled=true when all vars set")
	}
	if cfg.AppID != 42 {
		t.Errorf("AppID = %d, want 42", cfg.AppID)
	}
	if cfg.InstallationID != 7 {
		t.Errorf("InstallationID = %d, want 7", cfg.InstallationID)
	}
	if len(cfg.PrivateKeyPEM) == 0 {
		t.Error("PrivateKeyPEM is empty")
	}
}

// --- Config behavior: error when only some vars are set ---

func TestFromEnv_Error_WhenPartialVarsSet(t *testing.T) {
	// Only AppID set, others missing.
	t.Setenv(githubapp.EnvAppID, "42")
	t.Setenv(githubapp.EnvInstallationID, "")
	t.Setenv(githubapp.EnvPrivateKeyFile, "")

	_, _, err := githubapp.FromEnv()
	if err == nil {
		t.Fatal("expected error when only GITHUB_APP_ID is set")
	}
}

// --- Config behavior: error when key file is unreadable ---

func TestFromEnv_Error_WhenKeyFileUnreadable(t *testing.T) {
	t.Setenv(githubapp.EnvAppID, "42")
	t.Setenv(githubapp.EnvInstallationID, "7")
	t.Setenv(githubapp.EnvPrivateKeyFile, "/nonexistent/path/key.pem")

	_, _, err := githubapp.FromEnv()
	if err == nil {
		t.Fatal("expected error when key file does not exist")
	}
}
