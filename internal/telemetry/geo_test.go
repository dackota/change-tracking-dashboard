package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// traefikGeoIP2Headers are the header names the Traefik MaxMind GeoIP2 plugin
// sets, used here as a realistic configuration.
var traefikGeoIP2Headers = telemetry.GeoHeaders{
	CountryISOCode: "X-GeoIP2-Country",
	RegionISOCode:  "X-GeoIP2-Region",
	CityName:       "X-GeoIP2-City",
}

// TestMiddleware_GeoHeaders_OffByDefault verifies no geo attribute appears
// until header names are configured, even when a proxy is already setting the
// headers.
func TestMiddleware_GeoHeaders_OffByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	req.Header.Set("X-GeoIP2-Country", "US")

	attrs := spanAttributes(t, req)

	if _, ok := attrs["geo.country.iso_code"]; ok {
		t.Error("geo.country.iso_code was set without configured header names")
	}
}

// TestMiddleware_GeoHeaders_LiftedOntoSpan verifies configured headers become
// the OTel geo semantic-convention attributes.
func TestMiddleware_GeoHeaders_LiftedOntoSpan(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	req.Header.Set("X-GeoIP2-Country", "US")
	req.Header.Set("X-GeoIP2-Region", "US-CO")
	req.Header.Set("X-GeoIP2-City", "Denver")

	attrs := spanAttributes(t, req, telemetry.WithGeoHeaders(traefikGeoIP2Headers))

	want := map[attribute.Key]string{
		"geo.country.iso_code": "US",
		"geo.region.iso_code":  "US-CO",
		"geo.locality.name":    "Denver",
	}
	for key, wantValue := range want {
		if got := attrs[key].AsString(); got != wantValue {
			t.Errorf("span attribute %s = %q, want %q", key, got, wantValue)
		}
	}
}

// TestMiddleware_GeoHeaders_AbsentHeaderIsOmitted covers the failed-lookup
// case: a proxy that could not resolve an address sends the header empty (or
// not at all), and that must leave the dimension off the span rather than
// record "" as though it were a place.
func TestMiddleware_GeoHeaders_AbsentHeaderIsOmitted(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/trackers", nil)
	req.Header.Set("X-GeoIP2-Country", "US")
	req.Header.Set("X-GeoIP2-City", "")

	attrs := spanAttributes(t, req, telemetry.WithGeoHeaders(traefikGeoIP2Headers))

	if got := attrs["geo.country.iso_code"].AsString(); got != "US" {
		t.Errorf("geo.country.iso_code = %q, want US", got)
	}
	if _, ok := attrs["geo.locality.name"]; ok {
		t.Error("geo.locality.name was set from an empty header")
	}
	if _, ok := attrs["geo.region.iso_code"]; ok {
		t.Error("geo.region.iso_code was set from an absent header")
	}
}
