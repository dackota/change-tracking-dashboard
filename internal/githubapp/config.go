// Package githubapp — env-var based credential config loading.
package githubapp

import (
	"fmt"
	"os"
	"strconv"
)

// Env var names for GitHub App credentials.
const (
	EnvAppID          = "GITHUB_APP_ID"
	EnvInstallationID = "GITHUB_APP_INSTALLATION_ID"
	EnvPrivateKeyFile = "GITHUB_APP_PRIVATE_KEY_FILE"
)

// FromEnv reads GitHub App credentials from environment variables and the
// private-key file they point to. It returns:
//   - (cfg, true, nil) when all three env vars are set and the key file is readable.
//   - (zero, false, nil) when none of the three vars are set (GitHub App auth disabled).
//   - (zero, false, err) when some vars are set but others are missing or the key file
//     cannot be read — a configuration error the caller must surface.
func FromEnv() (Config, bool, error) {
	appIDStr := os.Getenv(EnvAppID)
	installIDStr := os.Getenv(EnvInstallationID)
	keyFile := os.Getenv(EnvPrivateKeyFile)

	// If none are set, GitHub App auth is disabled — not an error.
	if appIDStr == "" && installIDStr == "" && keyFile == "" {
		return Config{}, false, nil
	}

	// If at least one is set, all three must be provided.
	if appIDStr == "" {
		return Config{}, false, fmt.Errorf("githubapp: %s is set but %s is missing", EnvInstallationID, EnvAppID)
	}
	if installIDStr == "" {
		return Config{}, false, fmt.Errorf("githubapp: %s is set but %s is missing", EnvAppID, EnvInstallationID)
	}
	if keyFile == "" {
		return Config{}, false, fmt.Errorf("githubapp: %s is set but %s is missing", EnvAppID, EnvPrivateKeyFile)
	}

	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil || appID <= 0 {
		return Config{}, false, fmt.Errorf("githubapp: %s must be a positive integer", EnvAppID)
	}

	installID, err := strconv.ParseInt(installIDStr, 10, 64)
	if err != nil || installID <= 0 {
		return Config{}, false, fmt.Errorf("githubapp: %s must be a positive integer", EnvInstallationID)
	}

	privateKeyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		// Don't include file path in the error when it might be meaningful to hide;
		// the path is config, not secret, so it's fine to include here.
		return Config{}, false, fmt.Errorf("githubapp: read private key file %q: %w", keyFile, err)
	}

	return Config{
		AppID:          appID,
		InstallationID: installID,
		PrivateKeyPEM:  privateKeyPEM,
	}, true, nil
}
