// Package changesetquery answers "which Changesets does this request want?".
//
// It owns every decision between a raw set of query parameters and a page of
// classified Changesets: how a time window, facet filter, repo scope, impact
// and risk filters are parsed; how big a page may be; how the impact and risk
// predicates compose; which risk-rule snapshot both the filter and the
// rendered badges classify against; and how the cursor contract is reported.
//
// Those decisions used to live in the HTTP handler, which meant the only way
// to exercise them was an httptest round trip against a real SQLite file.
// They are query policy, not transport, so they live here: the handler
// decodes, delegates, and encodes.
package changesetquery

import (
	"context"
	"fmt"
	"net/url"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/dackota/change-tracking-dashboard/internal/changeset"
	"github.com/dackota/change-tracking-dashboard/internal/filter"
	"github.com/dackota/change-tracking-dashboard/internal/store"
	"github.com/dackota/change-tracking-dashboard/internal/telemetry"
)

// Source is the store seam this package reads through. *store.Store satisfies
// it directly; tests inject a fake, which is what lets the parsing, clamping,
// predicate-composition and rules-snapshot rules above be tested without a
// database.
type Source interface {
	// FacetOptions returns the known facet names and their observed values.
	// Query-param keys are whitelisted against this set before they can
	// become facet filters.
	FacetOptions() (map[string][]string, error)
	// QueryChangesets returns one page. pred may be nil, meaning "no
	// post-assembly filtering". End of results is signalled by an empty
	// NextCursor, never by a short page.
	QueryChangesets(w store.TimeWindow, spec filter.FilterSpec, pred store.ChangesetPredicate, cursor string, limit int) (store.ChangesetPage, error)
}

// Querier answers changeset queries against a Source.
type Querier struct {
	src   Source
	rules func() []changeset.RiskRule
}

// New builds a Querier over src. rules supplies the risk-rule set to classify
// against; it is called at most once per query (see Page.Rules for why that
// matters) and may be nil, in which case the built-in
// changeset.DefaultRiskRules are used.
func New(src Source, rules func() []changeset.RiskRule) *Querier {
	if rules == nil {
		rules = changeset.DefaultRiskRules
	}
	return &Querier{src: src, rules: rules}
}

// Page is one page of query results.
type Page struct {
	// Changesets is the page, newest first.
	Changesets []changeset.Changeset
	// Rules is the exact risk-rule snapshot the filter classified against.
	// Callers that render risk badges MUST classify against this value rather
	// than re-reading their rules source: the source is a live, hot-reloading
	// watcher, so a reload landing between filtering and rendering would
	// otherwise produce a response whose badges contradict the filter that
	// selected the changesets.
	Rules []changeset.RiskRule
	// NextCursor is the cursor for the following page, empty at the end of
	// results. End of results is signalled by this being empty and never by a
	// short page — a full page can still be the last one, and a short page can
	// still have more behind it.
	NextCursor string
	// Examined is how many commits the store's page-fill loop inspected to
	// produce this page. The ratio against len(Changesets) is what makes a
	// pathological filter diagnosable.
	Examined int
}

// Run parses q, queries, and returns one page.
//
// A malformed request — an unparseable time, a non-positive limit, an unknown
// impact/risk class, an invalid facet key, a corrupt cursor — is reported as
// an error wrapping ErrBadRequest. Every other error is a Source failure.
// Neither ever carries caller input back to the caller of Run; detail is for
// the server's logs.
func (q *Querier) Run(ctx context.Context, values url.Values) (Page, error) {
	params, err := q.parse(values)
	if err != nil {
		return Page{}, err
	}

	// Read the rules once. Both the filter predicate and the returned
	// snapshot come from this single value, so they can never disagree about
	// an operator's configured rules.
	rules := q.rules()
	warnUnreachableRiskFilter(telemetry.LoggerFromContext(ctx), params.Risks, rules)

	pred := allPredicates(
		impactPredicate(params.Impacts),
		riskPredicate(params.Risks, rules),
	)

	var page store.ChangesetPage
	err = telemetry.WithSpan(ctx, tracer(), "store.query_changesets", func(ctx context.Context) error {
		var err error
		page, err = q.src.QueryChangesets(params.Window, params.Spec, pred, params.Cursor, params.Limit)
		if err != nil {
			return err
		}
		// Record the page-fill loop's work: how many commits were examined
		// against how many survived. Without the examined count, a span
		// reporting 3 returned is indistinguishable from a cheap query that
		// returned 3 — and a filter that examined 5000 to find them is one
		// worth investigating.
		trace.SpanFromContext(ctx).SetAttributes(
			attribute.Int("changesets.examined", page.Examined),
			attribute.Int("changesets.returned", len(page.Changesets)),
		)
		return nil
	})
	if err != nil {
		// An invalid cursor is caller input, not a store failure, so it joins
		// the bad-request class rather than surfacing as an internal error.
		if isInvalidCursor(err) {
			return Page{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		return Page{}, fmt.Errorf("changesetquery: query changesets: %w", err)
	}

	return Page{
		Changesets: page.Changesets,
		Rules:      rules,
		NextCursor: page.NextCursor,
		Examined:   page.Examined,
	}, nil
}
