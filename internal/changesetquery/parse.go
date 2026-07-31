package changesetquery

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
)

// ErrBadRequest classifies every failure caused by the request itself rather
// than by the store. Callers map it to 400 and everything else to 500, via
// errors.Is — the concrete message is for server-side logs and never for the
// client, since it can quote caller input.
var ErrBadRequest = errors.New("changesetquery: bad request")

// MaxPageSize is the hard cap on Changesets returned per page, regardless of
// what a caller requests. A caller-dictated limit is never passed straight
// through to the store.
//
// This is the API's page size, distinct from — and required to stay at or
// below — store.MaxChangesetPageSize, which is the store's own absolute
// ceiling on a single fetch. TestMaxPageSize_WithinStoreCeiling pins that
// relationship so the two cannot drift into disagreement.
const MaxPageSize = 100

// DefaultPageSize is used when the caller omits the limit param.
const DefaultPageSize = 50

// params is a fully decoded, validated request.
type params struct {
	Window  store.TimeWindow
	Spec    filter.FilterSpec
	Impacts filter.ClassSet
	Risks   filter.ClassSet
	Cursor  string
	Limit   int
}

// parse decodes and validates every query parameter, reading the known facet
// vocabulary from the Source so unknown keys can be whitelisted out.
func (q *Querier) parse(values url.Values) (params, error) {
	asOf, err := parseAsOf(values.Get("asOf"))
	if err != nil {
		return params{}, fmt.Errorf("%w: parse asOf: %v", ErrBadRequest, err)
	}

	since, err := parseSince(values.Get("since"))
	if err != nil {
		return params{}, fmt.Errorf("%w: parse since: %v", ErrBadRequest, err)
	}

	// The known facet names gate which query-param keys may become filters at
	// all, so they are fetched before parsing rather than validated after.
	facetOpts, err := q.src.FacetOptions()
	if err != nil {
		return params{}, fmt.Errorf("changesetquery: facet options: %w", err)
	}

	spec, err := parseFilter(values, facetOpts)
	if err != nil {
		return params{}, fmt.Errorf("%w: parse facet filter: %v", ErrBadRequest, err)
	}
	// The repo scope is a single distinguished value, not a tri-state facet —
	// read from the reserved "repo" param and composed with the facet filter
	// via AND. An absent/empty repo param is a no-op: WithRepo("") matches any
	// repo.
	spec = spec.WithRepo(values.Get("repo"))

	// Impact and risk are flat allow-sets over closed vocabularies, not
	// facets, so they never reach filter.Parse. An unrecognized member is a
	// bad request rather than being silently dropped.
	impacts, err := filter.ParseClassSet(values["impact"], changeset.ImpactTiers())
	if err != nil {
		return params{}, fmt.Errorf("%w: parse impact filter: %v", ErrBadRequest, err)
	}

	// Risk parses against the slug vocabulary rather than the display values —
	// the wire never uses the display form.
	risks, err := filter.ParseClassSet(values["risk"], changeset.RiskSlugs())
	if err != nil {
		return params{}, fmt.Errorf("%w: parse risk filter: %v", ErrBadRequest, err)
	}

	limit, err := parseLimit(values.Get("limit"))
	if err != nil {
		return params{}, fmt.Errorf("%w: parse limit: %v", ErrBadRequest, err)
	}

	return params{
		Window:  store.TimeWindow{Since: since, AsOf: asOf},
		Spec:    spec,
		Impacts: impacts,
		Risks:   risks,
		Cursor:  values.Get("cursor"),
		Limit:   limit,
	}, nil
}

// parseFilter builds a filter.FilterSpec from the request's query params,
// restricted to known facet names minus the reserved ones. An unknown,
// non-reserved param name (a typo, an unrelated param) is silently ignored
// rather than rejected. The repo scope itself is applied by the caller via
// FilterSpec.WithRepo, not here.
func parseFilter(q url.Values, knownFacets map[string][]string) (filter.FilterSpec, error) {
	allowed := make(map[string]struct{}, len(knownFacets))
	values := make(map[string][]string, len(q))

	for name := range knownFacets {
		if store.IsReservedFacetName(name) {
			continue
		}
		allowed[name] = struct{}{}
		if vals, present := q[name]; present {
			values[name] = vals
		}
	}

	return filter.Parse(values, allowed)
}

// parseLimit parses the limit param and clamps it to MaxPageSize. An empty
// string yields DefaultPageSize. A non-positive or non-integer value is
// rejected as malformed rather than clamped — a caller asking for 0 or -1 has
// made a mistake, not expressed a preference.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return DefaultPageSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("limit must be positive, got %d", n)
	}
	if n > MaxPageSize {
		return MaxPageSize, nil
	}
	return n, nil
}

// parseAsOf parses the asOf param as RFC3339. An empty string defaults to
// "now" — the sensible reading of "show me the current state of the world"
// when the caller does not pin a point in time.
func parseAsOf(raw string) (time.Time, error) {
	if raw == "" {
		return time.Now(), nil
	}
	return time.Parse(time.RFC3339, raw)
}

// parseSince parses the since param as RFC3339 — the same format asOf
// accepts, so a caller never has to learn a second time format for one
// endpoint. An empty string yields the zero Time, which store.TimeWindow
// reads as "no lower bound".
//
// A since at or after asOf is deliberately not rejected. It describes an
// empty window, which the store answers with an empty page — a normal outcome
// for a polling loop, not a caller error.
func parseSince(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

// isInvalidCursor reports whether err is the store's invalid-cursor sentinel,
// which is caller input rather than a store failure.
func isInvalidCursor(err error) bool {
	return errors.Is(err, store.ErrInvalidCursor)
}

// instrumentationName scopes this package's tracer.
const instrumentationName = "github.com/dackota/change-tracking-dashboard/internal/changesetquery"

// tracer returns the tracer for the store-query span. It reads the ambient
// global provider on each call rather than capturing one at construction, so
// a Querier built before telemetry.Init still emits real spans afterwards.
func tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer(instrumentationName)
}
