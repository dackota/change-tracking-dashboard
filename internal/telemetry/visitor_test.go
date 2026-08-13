package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// day1 and day2 are consecutive UTC dates, used to exercise the daily
// rotation of visitor IDs.
var (
	day1 = time.Date(2026, 8, 12, 15, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
)

const testSalt = "test-salt"

// TestClientIP covers which address is treated as the client's, including the
// spoofing guard: X-Forwarded-For is honored only when the deployment has
// declared it trustworthy.
func TestClientIP(t *testing.T) {
	tests := []struct {
		name              string
		remoteAddr        string
		forwardedFor      string
		trustForwardedFor bool
		want              string
	}{
		{
			name:       "peer address with port has the port stripped",
			remoteAddr: "203.0.113.7:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "IPv6 peer address is unwrapped",
			remoteAddr: "[2001:db8::1]:54321",
			want:       "2001:db8::1",
		},
		{
			name:              "untrusted forwarded-for is ignored",
			remoteAddr:        "203.0.113.7:54321",
			forwardedFor:      "198.51.100.9",
			trustForwardedFor: false,
			want:              "203.0.113.7",
		},
		{
			name:              "trusted forwarded-for takes the leftmost entry",
			remoteAddr:        "10.0.0.1:54321",
			forwardedFor:      "198.51.100.9, 10.0.0.5, 10.0.0.1",
			trustForwardedFor: true,
			want:              "198.51.100.9",
		},
		{
			name:              "trusted but absent forwarded-for falls back to the peer",
			remoteAddr:        "10.0.0.1:54321",
			trustForwardedFor: true,
			want:              "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.forwardedFor != "" {
				r.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}

			if got := telemetry.ClientIP(r, tt.trustForwardedFor); got != tt.want {
				t.Errorf("ClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVisitorID_StableWithinADay is the property a unique-visitor count rests
// on: the same client on the same day must collapse to one identifier, or
// COUNT_DISTINCT counts requests rather than visitors.
func TestVisitorID_StableWithinADay(t *testing.T) {
	identity := telemetry.VisitorIdentity{Salt: testSalt}

	morning := telemetry.VisitorID(identity, "203.0.113.7", "Firefox", day1)
	evening := telemetry.VisitorID(identity, "203.0.113.7", "Firefox", day1.Add(8*time.Hour))

	if morning == "" {
		t.Fatal("VisitorID returned empty for a configured identity")
	}
	if morning != evening {
		t.Errorf("VisitorID differs within a single day: %q vs %q", morning, evening)
	}
}

// TestVisitorID_RotatesDaily guards the privacy property: no identifier
// follows a visitor across days, so the data cannot become long-term tracking.
func TestVisitorID_RotatesDaily(t *testing.T) {
	identity := telemetry.VisitorIdentity{Salt: testSalt}

	today := telemetry.VisitorID(identity, "203.0.113.7", "Firefox", day1)
	tomorrow := telemetry.VisitorID(identity, "203.0.113.7", "Firefox", day2)

	if today == tomorrow {
		t.Errorf("VisitorID did not rotate across days: %q on both", today)
	}
}

// TestVisitorID_DistinguishesClients verifies the inputs that should produce
// separate visitors actually do.
func TestVisitorID_DistinguishesClients(t *testing.T) {
	identity := telemetry.VisitorIdentity{Salt: testSalt}
	base := telemetry.VisitorID(identity, "203.0.113.7", "Firefox", day1)

	if other := telemetry.VisitorID(identity, "203.0.113.8", "Firefox", day1); other == base {
		t.Error("a different address produced the same visitor ID")
	}
	if other := telemetry.VisitorID(identity, "203.0.113.7", "Chrome", day1); other == base {
		t.Error("a different user agent produced the same visitor ID")
	}
	// A different salt must not reproduce another deployment's identifiers.
	otherDeployment := telemetry.VisitorIdentity{Salt: "different-salt"}
	if other := telemetry.VisitorID(otherDeployment, "203.0.113.7", "Firefox", day1); other == base {
		t.Error("a different salt produced the same visitor ID")
	}
}

// TestVisitorID_DisabledAndUnknownInputs verifies the two cases that must
// yield no identifier at all: identity not configured, and no client address.
// An empty salt must not silently degrade to an unsalted (reversible) hash.
func TestVisitorID_DisabledAndUnknownInputs(t *testing.T) {
	if got := telemetry.VisitorID(telemetry.VisitorIdentity{}, "203.0.113.7", "Firefox", day1); got != "" {
		t.Errorf("VisitorID with no salt = %q, want empty", got)
	}
	if got := telemetry.VisitorID(telemetry.VisitorIdentity{Salt: testSalt}, "", "Firefox", day1); got != "" {
		t.Errorf("VisitorID with unknown client address = %q, want empty", got)
	}
}
