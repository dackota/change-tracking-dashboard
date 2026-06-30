// Package githubapp_test exercises the token provider through its public interface
// using a mocked GitHub token endpoint (net/http/httptest) and an injectable clock.
package githubapp_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Panasonic-Global-Applied-AI/change-tracking-dashboard/internal/githubapp"
)

// generateTestKey creates a fresh RSA-2048 key for test use.
func generateTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

// tokenEndpointResponse is the JSON shape GitHub returns for an installation token.
type tokenEndpointResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// mockTokenServer returns an httptest.Server that responds to
// POST /app/installations/{id}/access_tokens with the given token and expiry.
// It records the number of requests to allow cache-hit assertions.
func mockTokenServer(t *testing.T, token string, expiresAt time.Time) (*httptest.Server, *int) {
	t.Helper()
	count := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*count++
		body, _ := json.Marshal(tokenEndpointResponse{
			Token:     token,
			ExpiresAt: expiresAt,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, count
}

// --- Behavior 1: provider returns a token from mocked endpoint ---

func TestTokenProvider_ReturnsToken(t *testing.T) {
	t.Parallel()

	privateKeyPEM := generateTestKey(t)
	expiry := time.Now().Add(1 * time.Hour)
	srv, _ := mockTokenServer(t, "ghs_test_token_1", expiry)

	clk := time.Now
	provider, err := githubapp.New(githubapp.Config{
		AppID:          42,
		InstallationID: 7,
		PrivateKeyPEM:  privateKeyPEM,
		BaseURL:        srv.URL,
		Clock:          clk,
	})
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}

	tok, err := provider.Token()
	if err != nil {
		t.Fatalf("provider.Token: %v", err)
	}
	if tok != "ghs_test_token_1" {
		t.Errorf("token = %q, want %q", tok, "ghs_test_token_1")
	}
}

// --- Behavior 2: token is cached within validity window ---

func TestTokenProvider_CachesToken(t *testing.T) {
	t.Parallel()

	privateKeyPEM := generateTestKey(t)
	expiry := time.Now().Add(1 * time.Hour)
	srv, reqCount := mockTokenServer(t, "ghs_cached_token", expiry)

	clk := time.Now
	provider, err := githubapp.New(githubapp.Config{
		AppID:          42,
		InstallationID: 7,
		PrivateKeyPEM:  privateKeyPEM,
		BaseURL:        srv.URL,
		Clock:          clk,
	})
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}

	// First call — fetches token.
	tok1, err := provider.Token()
	if err != nil {
		t.Fatalf("provider.Token (1): %v", err)
	}

	// Second call — should reuse the cached token without another request.
	tok2, err := provider.Token()
	if err != nil {
		t.Fatalf("provider.Token (2): %v", err)
	}

	if tok1 != tok2 {
		t.Errorf("cached token mismatch: %q vs %q", tok1, tok2)
	}
	if *reqCount != 1 {
		t.Errorf("expected 1 HTTP request (cache hit on second call), got %d", *reqCount)
	}
}

// --- Behavior 3: token is refreshed when the clock crosses the expiry threshold ---

func TestTokenProvider_RefreshesBeforeExpiry(t *testing.T) {
	t.Parallel()

	privateKeyPEM := generateTestKey(t)

	// We use a controllable clock. Start it at t=0.
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	mu := sync.Mutex{}
	clk := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advanceClock := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}

	// Token expires 60 minutes from now. The provider must refresh if the clock
	// is past (expiry - refreshBuffer). We'll test that after crossing the
	// refresh threshold a second request is made.
	expiry := time.Date(2024, 1, 1, 13, 0, 0, 0, time.UTC) // 60 min from t=0
	reqCount := 0
	token := "ghs_first_token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		body, _ := json.Marshal(tokenEndpointResponse{
			Token:     fmt.Sprintf("ghs_token_%d", reqCount),
			ExpiresAt: expiry,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
		_ = token
	}))
	t.Cleanup(srv.Close)

	provider, err := githubapp.New(githubapp.Config{
		AppID:          42,
		InstallationID: 7,
		PrivateKeyPEM:  privateKeyPEM,
		BaseURL:        srv.URL,
		Clock:          clk,
	})
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}

	// First call at t=0 — fetches token, expiry=13:00.
	tok1, err := provider.Token()
	if err != nil {
		t.Fatalf("provider.Token (1): %v", err)
	}
	if tok1 != "ghs_token_1" {
		t.Errorf("tok1 = %q, want ghs_token_1", tok1)
	}

	// Advance to 6 minutes before expiry (12:54) — more than refreshBuffer
	// remains, so the cached token must still be returned without a new request.
	advanceClock(54 * time.Minute)
	tok2, err := provider.Token()
	if err != nil {
		t.Fatalf("provider.Token (at 54min): %v", err)
	}
	if reqCount != 1 {
		t.Errorf("expected 1 request at 54min (still valid), got %d", reqCount)
	}
	if tok2 != tok1 {
		t.Errorf("expected cached token at 54min, got %q vs %q", tok2, tok1)
	}

	// Advance to 58 min total (2 min before expiry) — within the refreshBuffer,
	// so a second request must be issued and a new token returned.
	advanceClock(4 * time.Minute) // now at 58 min total
	tok3, err := provider.Token()
	if err != nil {
		t.Fatalf("provider.Token (at 59min): %v", err)
	}
	if reqCount != 2 {
		t.Errorf("expected 2 requests after crossing refresh threshold, got %d", reqCount)
	}
	if tok3 == tok1 {
		t.Errorf("expected a new token after refresh, still got %q", tok3)
	}
}

// --- Behavior 4: clear non-leaking error on non-200 from GitHub ---

func TestTokenProvider_ErrorOnNon201(t *testing.T) {
	t.Parallel()

	privateKeyPEM := generateTestKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Not Authorized"}`))
	}))
	t.Cleanup(srv.Close)

	provider, err := githubapp.New(githubapp.Config{
		AppID:          42,
		InstallationID: 7,
		PrivateKeyPEM:  privateKeyPEM,
		BaseURL:        srv.URL,
		Clock:          time.Now,
	})
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}

	_, err = provider.Token()
	if err == nil {
		t.Fatal("expected error on non-201 response, got nil")
	}

	// Error must not leak the private key, JWT, or token.
	errStr := err.Error()
	if contains(errStr, "BEGIN RSA") || contains(errStr, "PRIVATE KEY") {
		t.Errorf("error message leaks private key: %q", errStr)
	}
}

// --- Behavior 5: thread-safe concurrent access ---

func TestTokenProvider_ThreadSafe(t *testing.T) {
	t.Parallel()

	privateKeyPEM := generateTestKey(t)
	expiry := time.Now().Add(1 * time.Hour)
	srv, _ := mockTokenServer(t, "ghs_concurrent_token", expiry)

	provider, err := githubapp.New(githubapp.Config{
		AppID:          42,
		InstallationID: 7,
		PrivateKeyPEM:  privateKeyPEM,
		BaseURL:        srv.URL,
		Clock:          time.Now,
	})
	if err != nil {
		t.Fatalf("githubapp.New: %v", err)
	}

	const goroutines = 20
	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, err := provider.Token()
			errCh <- err
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent Token() call failed: %v", err)
		}
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
