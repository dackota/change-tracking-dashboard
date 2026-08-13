package telemetry

import (
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// GeoHeaders names the request headers a geo-aware proxy sets, so their values
// can be lifted onto the request span as OTel geo semantic-convention
// attributes. Each field is a header *name*; an empty field means "this
// dimension is not available", and no attribute is set for it. The zero value
// therefore disables geo enrichment entirely.
//
// Deriving location is deliberately not this service's job. Something upstream
// already has to hold a MaxMind database and keep it current, and doing that
// once at the proxy serves every service behind it, rather than bundling a
// ~60MB database and its monthly refresh into this binary's image. The app
// only reads what the proxy decided.
//
// Header names vary by plugin: the Traefik MaxMind GeoIP2 plugins set
// "X-GeoIP2-Country" or "GeoIP-Country-Code" style headers depending on which
// one is installed, so they are configured rather than hardcoded.
//
// These headers are trusted exactly as far as X-Forwarded-For is: a client
// talking directly to this service can set them to anything. Configure them
// only when a proxy that overwrites them is the sole route in.
type GeoHeaders struct {
	// CountryISOCode names the header carrying an ISO 3166-1 alpha-2 country
	// code ("US"), exported as geo.country.iso_code.
	CountryISOCode string

	// RegionISOCode names the header carrying an ISO 3166-2 subdivision code
	// ("US-CO"), exported as geo.region.iso_code.
	RegionISOCode string

	// CityName names the header carrying a city name ("Denver"), exported as
	// geo.locality.name. This is the highest-cardinality and least reliable of
	// the three — IP-to-city accuracy is poor — so prefer country unless a
	// question actually needs city granularity.
	CityName string
}

// enabled reports whether any geo header is configured.
func (g GeoHeaders) enabled() bool {
	return g.CountryISOCode != "" || g.RegionISOCode != "" || g.CityName != ""
}

// geoAttributes returns the geo attributes available for r under g. Headers
// that are unconfigured, absent, or empty contribute nothing: a missing
// lookup must leave the dimension off the span rather than record an empty
// string, which would otherwise read as a real value in a GROUP BY.
func geoAttributes(r *http.Request, g GeoHeaders) []attribute.KeyValue {
	if !g.enabled() {
		return nil
	}

	var attrs []attribute.KeyValue
	if value := r.Header.Get(g.CountryISOCode); g.CountryISOCode != "" && value != "" {
		attrs = append(attrs, semconv.GeoCountryISOCode(value))
	}
	if value := r.Header.Get(g.RegionISOCode); g.RegionISOCode != "" && value != "" {
		attrs = append(attrs, semconv.GeoRegionISOCode(value))
	}
	if value := r.Header.Get(g.CityName); g.CityName != "" && value != "" {
		attrs = append(attrs, semconv.GeoLocalityName(value))
	}
	return attrs
}
