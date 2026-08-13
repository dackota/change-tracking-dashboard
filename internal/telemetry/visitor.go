package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"
)

// visitorIDLength is how much of the hash is kept, in hex characters. 16 hex
// chars is 64 bits — far beyond collision risk at any plausible visitor count,
// and short enough to stay readable in a Honeycomb result table.
const visitorIDLength = 16

// VisitorIdentity configures the derivation of the "visitor.id" span
// attribute. The zero value is disabled: no identity attribute is set, which
// is the correct default for a service that has not decided how it wants to
// treat client addresses.
type VisitorIdentity struct {
	// Salt is mixed into the hash so a visitor ID cannot be reversed back to
	// an IP address by anyone who guesses the inputs (the IPv4 space is small
	// enough to brute-force an unsalted hash outright). It must be the same
	// across replicas or each replica derives a different ID for the same
	// visitor and unique counts multiply by the replica count.
	//
	// An empty Salt disables the attribute entirely rather than falling back
	// to an unsalted hash — a weak identifier here is a privacy liability,
	// not a degraded feature.
	Salt string

	// TrustForwardedFor takes the client address from the leftmost
	// X-Forwarded-For entry instead of the transport-level peer address.
	//
	// Enable this only when the service is reachable exclusively through a
	// proxy that overwrites the header, which is the case behind a Kubernetes
	// ingress. On a directly-reachable service the header is attacker-supplied,
	// and any client can forge unlimited distinct visitors.
	TrustForwardedFor bool
}

// enabled reports whether a visitor ID should be derived at all.
func (v VisitorIdentity) enabled() bool { return v.Salt != "" }

// ClientIP returns the client address for r: the leftmost X-Forwarded-For
// entry when trustForwardedFor is set and the header is present, otherwise the
// transport-level peer address. The port is stripped in both cases — it is a
// per-connection ephemeral value that would make every request look like a
// distinct client.
//
// It returns "" when no address can be determined, which callers must treat as
// "unknown" rather than as a distinct client.
func ClientIP(r *http.Request, trustForwardedFor bool) string {
	if trustForwardedFor {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// The leftmost entry is the original client; entries to its right
			// are the proxies the request passed through.
			first, _, _ := strings.Cut(forwarded, ",")
			if first = strings.TrimSpace(first); first != "" {
				return stripPort(first)
			}
		}
	}
	return stripPort(r.RemoteAddr)
}

// stripPort removes a trailing ":port" from a host:port pair, leaving bare
// addresses (which SplitHostPort rejects) untouched.
func stripPort(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// VisitorID derives the stable-but-anonymous identifier used to count unique
// visitors. It is the truncated SHA-256 of the salt, the client address, the
// User-Agent, and the UTC date.
//
// Including the date rotates every identifier at midnight UTC by construction:
// the same person on the same device is one visitor within a day and a fresh
// one tomorrow. That is both the conventional definition of a "daily unique
// visitor" and the property that keeps this from being long-term tracking —
// there is no identifier that follows anyone across days.
//
// The User-Agent is mixed in because it splits some of the visitors that share
// a single NAT or corporate egress address. It is a heuristic, not a
// guarantee: two identical browsers behind one address still count once, and
// one person on phone and laptop counts twice.
//
// It returns "" when identity is disabled or the client address is unknown.
func VisitorID(identity VisitorIdentity, clientIP, userAgent string, now time.Time) string {
	if !identity.enabled() || clientIP == "" {
		return ""
	}

	// NUL separators keep the fields unambiguous, so distinct inputs cannot
	// concatenate into the same string and collide.
	h := sha256.New()
	for _, field := range []string{identity.Salt, clientIP, userAgent, now.UTC().Format(time.DateOnly)} {
		h.Write([]byte(field))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:visitorIDLength]
}
