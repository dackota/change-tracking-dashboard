// Package githubapp provides a GitHub App installation token provider.
//
// The Provider mints a short-lived installation access token by:
//  1. Building an RS256-signed App JWT from the App ID + private key (via
//     ghinstallation.AppsTransport, which handles the JWT plumbing).
//  2. Calling POST /app/installations/{id}/access_tokens on the GitHub API,
//     authenticated with that JWT.
//
// Tokens are cached and automatically refreshed before expiry (when less than
// refreshBuffer time remains). The provider is thread-safe.
//
// SECURITY: the private key, App JWT, and installation token are never
// included in error messages or log output.
package githubapp

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

// defaultBaseURL is the GitHub API base, used when no override is provided.
const defaultBaseURL = "https://api.github.com"

// refreshBuffer is how long before expiry the provider proactively refreshes
// the cached token so callers never use one that is about to expire mid-request.
const refreshBuffer = 5 * time.Minute

// httpClientTimeout bounds the GitHub API call so it never hangs indefinitely.
const httpClientTimeout = 15 * time.Second

// Config holds the credentials and runtime options for a Provider.
type Config struct {
	// AppID is the GitHub App's numeric ID.
	AppID int64
	// InstallationID is the installation ID of the App in the target org/repo.
	InstallationID int64
	// PrivateKeyPEM is the PEM-encoded RSA private key for the GitHub App.
	// It is consumed once at construction time and not retained as-is.
	PrivateKeyPEM []byte
	// BaseURL overrides the GitHub API base URL (default: https://api.github.com).
	// Tests point this at an httptest.Server to avoid real network calls.
	BaseURL string
	// Clock returns the current time. Defaults to time.Now when nil.
	// Tests inject a fake clock to drive refresh-before-expiry deterministically.
	Clock func() time.Time
}

// installationTokenResponse is the JSON shape of GitHub's access_tokens response.
type installationTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// cachedToken holds the currently valid installation token and its expiry.
type cachedToken struct {
	value     string
	expiresAt time.Time
}

// Provider mints and caches GitHub App installation tokens.
// It is safe for concurrent use.
type Provider struct {
	// httpClient has the appsTransport as its Transport so that every request
	// it makes is signed with the RS256 App JWT.
	httpClient *http.Client
	installID  int64
	baseURL    string
	clock      func() time.Time

	mu     sync.Mutex
	cached *cachedToken
}

// New constructs a Provider from the given Config.
// It validates the private key PEM but does not make any network calls.
func New(cfg Config) (*Provider, error) {
	if cfg.AppID == 0 {
		return nil, fmt.Errorf("githubapp: AppID must be non-zero")
	}
	if cfg.InstallationID == 0 {
		return nil, fmt.Errorf("githubapp: InstallationID must be non-zero")
	}
	if len(cfg.PrivateKeyPEM) == 0 {
		return nil, fmt.Errorf("githubapp: PrivateKeyPEM must not be empty")
	}

	// Validate the PEM key before proceeding (fail fast with a clear error).
	block, _ := pem.Decode(cfg.PrivateKeyPEM)
	if block == nil {
		return nil, fmt.Errorf("githubapp: PrivateKeyPEM is not a valid PEM block")
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		return nil, fmt.Errorf("githubapp: invalid RSA private key: %w", err)
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	// AppsTransport signs HTTP requests with the RS256 App JWT.
	// We use http.DefaultTransport as the underlying transport; tests replace
	// the Client on the provider after construction.
	atr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.AppID, cfg.PrivateKeyPEM)
	if err != nil {
		// Don't propagate error details — the key is already validated above.
		return nil, fmt.Errorf("githubapp: failed to initialize App transport")
	}
	atr.BaseURL = baseURL

	clk := cfg.Clock
	if clk == nil {
		clk = time.Now
	}

	return &Provider{
		httpClient: &http.Client{
			Transport: atr,
			Timeout:   httpClientTimeout,
		},
		installID: cfg.InstallationID,
		baseURL:   baseURL,
		clock:     clk,
	}, nil
}

// Token returns the current installation token, fetching or refreshing it
// as needed. It is safe for concurrent use.
//
// The token is refreshed proactively once fewer than refreshBuffer minutes
// remain before expiry. On failure, a clear non-leaking error is returned.
func (p *Provider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.clock()

	// Serve from cache if we have a token with more than refreshBuffer remaining.
	if p.cached != nil && now.Add(refreshBuffer).Before(p.cached.expiresAt) {
		return p.cached.value, nil
	}

	resp, err := p.fetchInstallationToken()
	if err != nil {
		return "", err
	}

	p.cached = &cachedToken{
		value:     resp.Token,
		expiresAt: resp.ExpiresAt,
	}
	return p.cached.value, nil
}

// fetchInstallationToken calls POST /app/installations/{id}/access_tokens,
// authenticated with the App JWT (via appsTransport), and decodes the response.
func (p *Provider) fetchInstallationToken() (*installationTokenResponse, error) {
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens",
		strings.TrimRight(p.baseURL, "/"), p.installID)

	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("githubapp: build token request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	// p.httpClient has the appsTransport wired as its Transport, so it attaches
	// the RS256-signed App JWT Authorization header before each request.
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("githubapp: token endpoint request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("githubapp: token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tokenResp installationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("githubapp: decode token response: %w", err)
	}

	if tokenResp.Token == "" {
		return nil, fmt.Errorf("githubapp: token endpoint returned empty token")
	}

	return &tokenResp, nil
}
