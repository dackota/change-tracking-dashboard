# Change Tracking Dashboard

Watch what actually changes across your GitOps repos. The dashboard polls the git
history of your config repositories, extracts the fields you care about like Helm
chart/subchart versions, image tags, Terraform inputs — and surfaces every change
as a faceted, time-ordered feed. Click any change to see the real impact: the
rendered Helm manifest diff or the Terraform resource-change view for that commit.

![Change Tracking Dashboard demo](docs/demo.gif)

**Live demo:** <https://changes.dackota.com>

## Why

A version bump in a `Chart.yaml` or `values.yaml` is one line in a diff, but it can
change hundreds of lines of rendered Kubernetes manifests. This tool answers three
questions that a plain `git log` can't:

- **What changed, and when?** A timeline of every tracked field change, grouped by
  commit, across all your repos.
- **What was the real blast radius?** Chart-version bumps are rendered to their
  full manifest diff; Terraform changes are shown as a static, credential-free
  resource-change view — no cluster or cloud access required.
- **Is anything risky?** Changes are classified and flagged (replace/destroy,
  security-sensitive, cost tripwires) right in the feed.

## Features

- **Timeline + feed** — every change in commit order, with an interactive timeline
  chart (drag to zoom, scroll, click a cluster to expand) and headline KPIs.
- **Faceted filtering** — filter the feed by repository or by facets you define
  (e.g. `component`, `layer`) with include/exclude toggles.
- **Rendered Helm chart diffs** — a chart/subchart version bump expands to the
  actual rendered-manifest delta between the old and new version.
- **Terraform plan diffs** — a static, credential-free resource-change view for
  HCL changes, classified by kind and risk.
- **Impact tiers** — every changeset carries an always-present `major` /
  `minor` / `patch` / `downgrade` / `other` badge derived from the semantic-
  version delta of its changes — no configuration, no migration (see
  [Impact tiers](#impact-tiers)).
- **Risk badges** — replace/destroy, security, and cost-tripwire signals
  surfaced on the feed, orthogonal to Impact. The rule set is configurable
  (see [Risk rules](#risk-rules)).
- **Issue/PR links** — changesets link back to the issues referenced in their
  commit messages.
- **Repositories & Trackers views** — per-repo rollups and a live view of your
  tracker configuration and poll health.
- **Private repos** — optional GitHub App auth using short-lived installation
  tokens.
- **Operable** — OpenTelemetry traces/metrics/logs, a `/healthz` liveness probe,
  hot-reloaded config, and a single static binary in a distroless image.

## How it works

```
config.yaml ─▶ poller ─▶ field extractor (jq / HCL) ─▶ change detector ─▶ SQLite ─▶ web UI
   (trackers)   (git)      (per file glob + field)       (diff by key)    (store)   (timeline)
```

For each tracker the poller walks the repo's git history on a cadence, extracts the
configured fields from files matching each glob at every commit, and records a
**Change** whenever a tracked value differs from the previous commit. All changes
from a single commit form a **Changeset**. Chart and Terraform diffs are rendered
on demand when you open a changeset.

## Quick start

The dashboard is a single binary configured by a YAML file plus a few environment
variables.

### Run with Docker

```bash
docker run --rm -p 8080:8080 \
  -v "$PWD/config.yaml:/etc/dashboard/config.yaml:ro" \
  -v "ctd-data:/data" \
  -e DB_PATH=/data/changes.db \
  ghcr.io/dackota/change-tracking-dashboard:latest
```

Then open <http://localhost:8080>.

### Run from source

Requires Go 1.26+.

```bash
CONFIG_PATH=./config.yaml DB_PATH=./changes.db \
  go run ./cmd/dashboard
```

## Configuration

### Environment variables

| Variable                       | Default                       | Purpose                                                        |
| ------------------------------ | ----------------------------- | ------------------------------------------------------------- |
| `CONFIG_PATH`                  | `/etc/dashboard/config.yaml`  | Path to the tracker config file (watched and hot-reloaded).   |
| `DB_PATH`                      | `changes.db`                  | Path to the SQLite database file (created if missing).        |
| `LISTEN_ADDR`                  | `:8080`                       | HTTP listen address.                                          |
| `OTEL_EXPORTER_OTLP_ENDPOINT`  | *(unset)*                     | OTLP endpoint for traces/metrics; overrides the config value. |
| `OTEL_EXPORTER_OTLP_HEADERS`   | *(unset)*                     | Comma-separated `key=value` headers sent on every export.      |
| `HONEYCOMB_API_KEY`            | *(unset)*                     | Honeycomb ingest key; shorthand for an `x-honeycomb-team` header. |
| `VISITOR_ID_SALT`              | *(unset)*                     | Enables the `visitor.id` span attribute; unset disables it.    |
| `PERSISTENT_VISITOR_COOKIE`    | `false`                       | Set to `true` to enable the `visitor_id` cookie and `visitor.persistent_id` span attribute. |
| `TRUST_FORWARDED_FOR`          | `false`                       | Take the client address from `X-Forwarded-For` (set only behind a proxy). |
| `GEO_COUNTRY_HEADER`           | *(unset)*                     | Header carrying an ISO country code, e.g. `X-GeoIP2-Country`.  |
| `GEO_REGION_HEADER`            | *(unset)*                     | Header carrying an ISO 3166-2 subdivision code.                |
| `GEO_CITY_HEADER`              | *(unset)*                     | Header carrying a city name.                                   |
| `GITHUB_APP_ID`                | *(unset)*                     | Enables GitHub App auth for private repos (see below).         |
| `GITHUB_APP_INSTALLATION_ID`   | *(unset)*                     | GitHub App installation ID.                                    |
| `GITHUB_APP_PRIVATE_KEY_FILE`  | *(unset)*                     | Path to the GitHub App private key (PEM).                      |

### Tracker config file

The config file defines global defaults, optional telemetry, and one or more
**trackers**. Each tracker points at a repo, extracts fields from files matching
globs, and (optionally) derives facets from file paths. The file is watched:
edits take effect on the next poll cycle without a restart.

```yaml
# Global defaults; any tracker may override these.
defaults:
  pollIntervalSeconds: 600      # poll each repo every 10 minutes
  backfillDays: 90              # walk 90 days of history on first run

# Optional. Empty (or omitted) disables telemetry export safely.
# OTEL_EXPORTER_OTLP_ENDPOINT overrides this when set.
observability:
  otlp_endpoint: ""             # e.g. "otel-collector:4317"

trackers:
  - repo: https://github.com/your-org/your-gitops.git
    # Named capture groups become facets in the UI. A path like
    # gitops/platform/argocd/... yields layer=platform, component=argocd.
    facetRegex: '^gitops/(?P<layer>[^/]+)/(?P<component>[^/]+)/'
    files:
      # Track Helm chart/subchart versions declared in Chart.yaml.
      - glob: 'gitops/*/*/Chart.yaml'
        fields:
          - name: chartDependencies
            expr: '.dependencies | map({(.name): .version}) | add'
      # Track image tags declared in values.yaml.
      - glob: 'gitops/*/*/values.yaml'
        fields:
          - name: imageTags
            expr: 'to_entries | map(select(.value.image.tag)) | map({(.key): .value.image.tag}) | add'
```

Field reference:

- `defaults.pollIntervalSeconds` / `defaults.backfillDays` — global cadence and
  first-run history window; a tracker may override either.
- `trackers[].repo` — a local path or an `https://` URL. `http://` is rejected.
- `trackers[].facetRegex` — a regex applied to matched file paths; each named
  capture group becomes a filterable facet in the UI. Leave empty for no facets.
- `trackers[].files[].glob` — file glob, relative to the repo root.
- `trackers[].files[].fields[].name` — the human-readable field label shown on
  changes.
- `trackers[].files[].fields[].expr` — a [jq](https://jqlang.github.io/jq/)
  expression (evaluated by [gojq](https://github.com/itchyny/gojq)) that extracts
  the tracked value from the parsed file.
- `trackers[].engine` — extractor backend: `jq`, `hcl` (for Terraform/HCL files),
  or omit to auto-detect from the file glob.

### Impact tiers

Every changeset (and every individual change within it) carries an **impact**
tier, computed at read time from the semantic-version delta of the change's old
and new values — never stored, never configured, and never blank:

| Tier | Meaning | Example |
| --- | --- | --- |
| `major` | Breaking upgrade | `1.9.0 → 2.0.0` |
| `minor` | New functionality | `1.20.3 → 1.21.0` |
| `patch` | Fix-level bump | `10.1.2 → 10.1.3` |
| `downgrade` | Version moved backwards (rollback) | `2.0.0 → 1.9.0` |
| `other` | Not a comparable version change | add/remove, `~>7.0 → ~>8.0`, node count `2 → 3` |

A changeset's badge is the highest-precedence tier among its changes:

```
major > downgrade > minor > patch > other
```

A rollback is more notable than a routine forward minor/patch bump, but a
major version jump is still the headline for a changeset containing both.

Impact requires **no configuration** — it works out of the box on upgrade,
with no schema migration and no re-poll, since it's derived fresh on every
read (the same way `Kind` already is). It is **orthogonal to Risk**: a
changeset carries one impact tier *and* zero-or-more risk flags — a
cost-tripwire node-pool resize still shows its risk badge alongside whatever
impact tier the resize itself carries.

Because Impact now owns the major-version signal, the risk rule set's
previous `major version bump` default was removed (see [Risk
rules](#risk-rules) below for how to re-add it explicitly via `riskRules` if
you relied on that badge specifically).

### Risk rules

Every changeset is classified into zero or more **risk** classes at read time and
badged in the feed, alongside (not instead of) its Impact tier. Classification is
data-driven: a rule fires when *all* of its predicates match a change in the
changeset.

The dashboard ships a built-in default rule set: replace/destroy, security, and
cost-tripwire, tuned to the OCI dogfood conventions. It no longer ships a
default major-version-bump rule — `impact: major` (see [Impact
tiers](#impact-tiers) above) carries that signal now, so a major bump earns
exactly one badge instead of two saying the same thing. The `semverBump`
predicate itself is still fully supported for a config-authored rule (see
below) — only the shipped default was removed. Bare integers (a node count `2`
→ `3`) are treated as quantities, not versions, and never trip the semver
predicate.

If you relied on the removed default, re-add it explicitly:

```yaml
riskRules:
  - name: semver-major-bump
    risk: major version bump
    semverBump: major
```

An optional top-level `riskRules:` block adds your own rules. Configured rules
**augment** the built-ins (they don't replace them) and are hot-reloaded with the
rest of the config. Each rule supports these predicate fields — every one is
optional, and an omitted field matches anything along that dimension:

```yaml
riskRules:
  # Flag a MAJOR version jump only for Helm chart dependencies.
  - name: chart-major-bump
    risk: major version bump        # badge label (any non-empty string)
    kinds: [chart]                  # chart | value | provider | module | resource | variable
    changeTypes: [modified]         # added | removed | modified
    fieldPattern: chartDependencies # Go regexp matched against the field name
    semverBump: major               # fires only on a major-version increase

  # Flag any change that opens a firewall to the world.
  - name: open-to-world
    risk: security
    valuePattern: '0\.0\.0\.0/0'    # Go regexp matched against the new (or, if
                                    # removed, old) value
```

Field reference:

- `name` — documents the rule; not matched against anything.
- `risk` — the badge label emitted when the rule fires (required, non-empty).
- `kinds` / `changeTypes` — restrict to those change kinds / types; unknown values
  are rejected at load.
- `filePathPattern` / `fieldPattern` / `valuePattern` — Go [regexp](https://pkg.go.dev/regexp/syntax)
  matched (substring, unless anchored) against the change's file path, field name,
  and value respectively; an invalid pattern is rejected at load.
- `semverBump` — `major` fires only when the old and new values are both valid
  semver and the major component increases. Omit for no version constraint.

An invalid rule (bad regexp, unknown kind/changeType/semverBump, missing `risk`)
fails config load with an actionable error rather than silently never firing.

## HTTP endpoints

| Path                    | Description                                      |
| ----------------------- | ------------------------------------------------ |
| `/`                     | Timeline (KPIs, timeline chart, change feed).    |
| `/changes`              | The change feed on its own.                      |
| `/repositories`         | Per-repository rollups.                          |
| `/trackers`             | Configured trackers and per-tracker poll health. |
| `/healthz`              | Liveness check (no dependencies).                |
| `/api/changesets*`      | JSON + HTML fragments backing the UI.            |

The sections below walk through each endpoint with worked examples. For the
field-by-field contract — full response schemas, enum vocabularies, error
bodies, and security headers — see [docs/api.md](docs/api.md).

### `GET /api/changesets`

Returns changesets newest-first as JSON, with cursor pagination.

| Param    | Description                                                            |
| -------- | ---------------------------------------------------------------------- |
| `since`  | RFC3339. **Inclusive** lower bound on commit time. Omit for no bound.  |
| `asOf`   | RFC3339. **Exclusive** upper bound on commit time. Defaults to now.    |
| `repo`   | Restrict to one tracked repository.                                     |
| `cursor` | Opaque `nextCursor` from a previous response. Omit for the first page. |
| `limit`  | Page size, clamped to 100. Defaults to 50.                             |
| `impact` | Restrict to one or more impact tiers. Repeatable. Omit for no filter.  |
| `risk`   | Restrict to one or more risk classes, by slug. Repeatable.             |
| *facets* | Any configured facet name, as include/exclude filters.                  |

Together `since` and `asOf` form a **half-open window** `[since, asOf)`. This is
what makes incremental polling cheap and correct: feed one request's `asOf`
straight back as the next request's `since` and consecutive windows tile the
timeline exactly once — no gaps, no duplicates, and no timestamp arithmetic on
your side. A changeset committed at exactly `since` is returned; one committed
at exactly `asOf` is not.

```bash
curl "https://changes.dackota.com/api/changesets?since=2026-07-29T00:00:00Z&asOf=2026-07-30T00:00:00Z"
```

A `since` at or after `asOf` describes an empty window and returns an empty list
with `200` — a normal outcome for a polling loop, not an error. A malformed
`since` returns `400`.

Keep following `nextCursor` until it comes back empty; a non-empty cursor is the
only signal that more results exist.

#### Filtering by impact

`impact` restricts the feed to one or more impact tiers: `major`, `minor`,
`patch`, `downgrade`, or `other`. Repeated values OR together, and the result
ANDs with `repo` and any facet filters.

```bash
# every breaking or rolled-back change in infra-repo's prod environment
curl "https://changes.dackota.com/api/changesets?impact=major&impact=downgrade&repo=infra-repo&env=prod"
```

**An unrecognized `impact` value returns `400`.** This is a deliberate
divergence from how unknown *facet* params are handled, which are silently
ignored. The reasoning: a facet vocabulary is open-ended and data-derived, so
ignoring an unknown key is the only sane option — but the impact vocabulary is
closed and five values long. Silently ignoring `impact=majr` would return the
whole unfiltered feed, and a consumer that alerts on the response would read
that as "everything is major". A rejection is the far better failure.

Because `impact` is evaluated after changesets are assembled rather than in
SQL, a filtered request has a per-call bound on how many commits it will
examine. A highly selective filter can therefore return a **short page that is
not the last page**. This changes nothing about how you paginate — keep
following `nextCursor` until it is empty — but never infer that you are done
from a page being shorter than `limit`.

#### Filtering by risk

`risk` restricts the feed to changesets carrying one or more risk classes.
Because two of the display values are awkward in a URL, the wire uses stable
slugs:

| Slug                 | Display value        |
| -------------------- | -------------------- |
| `replace-destroy`    | `replace/destroy`    |
| `security`           | `security`           |
| `cost-tripwire`      | `cost tripwire`      |
| `major-version-bump` | `major version bump` |

Only the slug is accepted on the wire; the display value is not. Repeated
values OR together, and the result ANDs with `impact`, `repo`, and any facet
filters. A changeset matches when its risk set **intersects** the requested
set, so a changeset carrying several classes matches a query naming any one of
them — and a changeset carrying no risk at all never matches a non-empty
`risk` filter.

```bash
# security or cost-relevant changes in prod, that are also breaking
curl "https://changes.dackota.com/api/changesets?risk=security&risk=cost-tripwire&impact=major&env=prod"
```

The `risk[]` field in the **response** continues to carry display values, not
slugs — slugs are request vocabulary only.

As with `impact`, an unrecognized `risk` value returns `400` rather than being
silently ignored, and the same short-page caveat applies.

Risk classification is driven by operator-configured rules, so which
changesets a `risk` query returns depends on your configured rule set. The
built-in default rules produce only `replace-destroy`, `security`, and
`cost-tripwire`. `major-version-bump` is **not** produced by default — that is
deliberate, since `impact=major` already carries the signal (see [Risk
classes](#risk-classes)) — so it requires a configured rule using `semverBump`.

That matters when reading an empty result. A slug in the vocabulary is always
accepted, so `?risk=major-version-bump` on a default deployment returns `200`
with an empty list, which looks identical to "no breaking upgrades in this
window" but actually means "no rule here produces that class". To make the
difference visible, the server logs a warning naming the class and the fix
whenever a `risk` filter names a class no active rule can produce:

```
level=WARN message="web: risk filter names a class no configured rule can produce; ..." risk=major-version-bump remedy="add a riskRules entry for this class ..."
```

If you see that warning and want the class, add the rule shown above under
[Risk classes](#risk-classes). If you filter only on classes your rules
produce, you will never see it.

### `GET /api/changesets/detail`

Returns a single changeset — every change that commit produced — identified by
`repo` and `commitSha` (both required).

This endpoint serves **two representations, selected by the `Accept` header**:

| `Accept` contains                   | Response                        |
| ----------------------------------- | ------------------------------- |
| `application/json` (explicitly)     | JSON                            |
| anything else, including `*/*`      | HTML fragment (the default)     |

```bash
curl -H "Accept: application/json" \
  "https://changes.dackota.com/api/changesets/detail?repo=infra-repo&commitSha=abc123"
```

**A wildcard is deliberately not treated as opting in.** `*/*` gets HTML, and
so does an absent `Accept` header. This is not strict-by-pedantry: the
dashboard's own UI fetches this endpoint with `XMLHttpRequest` and never sets
an `Accept` header, so the browser sends `*/*`. If a wildcard counted as
asking for JSON, the live UI would receive JSON where it expects HTML
fragments to splice into the page. If you want JSON, name it.

The JSON body is the **same changeset shape the list endpoint emits** —
identical field names, including the computed `risk[]` and `impact`
projections — so one parser handles both endpoints.

When JSON is negotiated, errors are JSON objects too, so a single code path
parses every response:

```json
{ "error": "not found" }
```

Status codes are the same in both representations: `400` when `repo` or
`commitSha` is missing, `404` for an unknown changeset, `500` on an internal
failure. Error messages are generic and never echo request values back, and a
`404` reveals nothing about whether the repo or the commit exists.

### `GET /api/changesets/detail/chart-diff`

Returns the manifest-level blast radius of a Helm chart version bump — what
actually changed in the rendered Kubernetes manifests, not just the version
string. Requires `repo`, `commitSha`, and `path` (the chart's directory).

It follows the same `Accept` rule as `/api/changesets/detail`: JSON only when
you name `application/json` explicitly; `*/*`, an absent header, and
`text/html` all get today's HTML fragment.

```bash
curl -H "Accept: application/json" \
  "https://changes.dackota.com/api/changesets/detail/chart-diff?repo=infra-repo&commitSha=abc123&path=workloads/app"
```

Every response carries a `kind` classifying the outcome:

| `kind`              | Meaning                                                       |
| ------------------- | ------------------------------------------------------------- |
| `ok`                | The diff computed; `diff` is present.                          |
| `no-prior-version`  | Root commit — there is no "old" side to diff against.          |
| `unavailable`       | A chart dependency has no vendored artifact, and this service never pulls from a registry. |
| `could-not-render`  | The chart could not be rendered (malformed content, or any other failure). |
| `exceeded-limits`   | The render hit a configured timeout or resource ceiling.       |

On `ok`, the body carries the unified diff text, its summary counts, and the
truncation flag — so you can present either the counts or the full diff
depending on how much room you have:

```json
{
  "kind": "ok",
  "diff": {
    "unified": "-image: 1.9.0\n+image: 2.0.0\n",
    "truncated": false,
    "summary": { "manifestsChanged": 12, "linesAdded": 200, "linesRemoved": 140 }
  }
}
```

`truncated` is `true` when `unified` was cut short by the size ceiling —
**check it before presenting a diff as complete.** The `summary` counts are
always the true totals, computed before any truncation, so they stay an honest
blast-radius indicator either way.

**Every non-`ok` outcome carries the `kind` and nothing else.** `diff` is
omitted entirely — no error strings, no Helm output, no git internals ever
reach the wire. The underlying cause stays server-side in the logs.

```json
{ "kind": "could-not-render" }
```

Status codes match the sibling endpoints: `400` when `repo`, `commitSha`, or
`path` is missing, `404` for an unknown changeset, `500` on an internal
failure; errors are JSON objects when JSON is negotiated. A `404` is returned
both for an unknown changeset and for a `path` that isn't the directory of one
of that changeset's own chart changes — the two are **indistinguishable**, in
both representations, so the endpoint can't be used to enumerate what has been
ingested.

### `GET /api/changesets/detail/plan-diff`

The Terraform counterpart: the static resource-level blast radius of a change,
so a PR bot can post "2 resources force replacement" as a check and reviewers
see destructive changes before merging rather than after. Requires `repo`,
`commitSha`, and `path`. Same `Accept` rule, same status codes, same
indistinguishable `404`.

```bash
curl -H "Accept: application/json" \
  "https://changes.dackota.com/api/changesets/detail/plan-diff?repo=infra-repo&commitSha=abc123&path=envs/prod"
```

The `kind` vocabulary is the chart-diff one minus `unavailable`: `ok`,
`no-prior-version`, `could-not-render`, `exceeded-limits`. There is no
`unavailable` case here — a Terraform resource block is always statically
resolvable from the materialized subtree, so there is nothing to decline to
fetch.

On `ok`, the body carries the unified diff and line summary, the aggregate
resource counts, and the per-resource deltas:

```json
{
  "kind": "ok",
  "diff": {
    "unified": "-shape = \"VM.Standard2.1\"\n+shape = \"VM.Standard2.4\"\n",
    "truncated": false,
    "summary": { "manifestsChanged": 3, "linesAdded": 9, "linesRemoved": 4 }
  },
  "summary": { "added": 1, "removed": 1, "changed": 1, "replaced": 2 },
  "resources": [
    { "type": "oci_core_instance", "name": "web", "kind": "changed", "forcesReplacement": true },
    { "type": "oci_core_vcn", "name": "main", "kind": "added", "forcesReplacement": false },
    { "type": "oci_load_balancer", "name": "edge", "kind": "removed", "forcesReplacement": true }
  ]
}
```

`summary` is the aggregate, so a one-line blast-radius summary needs no
client-side computation. `replaced` counts how many of `removed` + `changed`
force replacement — it is a **subset of those two, never a separate category**,
so `added + removed + changed` is the total resource count and adding
`replaced` would double-count.

Each entry in `resources` carries the resource's `type` and `name` (its HCL
address), its change `kind` (`added` / `removed` / `changed`), and
`forcesReplacement`. That last flag is the destructive-change signal: `true`
for a removal (always destructive) or a change touching a force-replacement
attribute, and always `false` for an addition. `resources` is returned in a
deterministic sorted order, so output is stable across requests and two
responses can be diffed meaningfully.

As with chart-diff, a non-`ok` outcome carries the `kind` and nothing else —
`diff`, `summary`, and `resources` are all omitted, and no HCL-parser or git
internals reach the wire.

## Telemetry

The dashboard emits OpenTelemetry traces and RED metrics (rate, errors,
duration) over OTLP/gRPC, plus structured logs carrying `trace_id`/`span_id`
for correlation. With no endpoint configured the SDK still initializes and
spans still carry real IDs — nothing is exported, and nothing breaks.

Transport is chosen from the endpoint: an `https://` scheme or a bare
`host:443` dials TLS, anything else (`otel-collector:4317`) stays plaintext.

### Sending to Honeycomb

Point the endpoint at Honeycomb and supply an ingest key. No collector needed:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=api.honeycomb.io:443
export HONEYCOMB_API_KEY=your-ingest-key
```

Use `https://api.eu1.honeycomb.io` for the EU region. Traces route to a
dataset named after `service.name` (`change-tracking-dashboard`).

`HONEYCOMB_API_KEY` is shorthand for `OTEL_EXPORTER_OTLP_HEADERS=x-honeycomb-team=...`;
set the header variable directly if you need to send more than one header. The
key is an ingest credential — inject it from a Kubernetes Secret, not from the
tracker ConfigMap. At startup the dashboard logs the endpoint and the *names*
of the configured headers (never their values), so a missing key is
diagnosable: Honeycomb rejects unauthenticated exports silently.

### Counting unique visitors

Request spans carry `http.request.method`, `http.route`, `http.response.status_code`,
`url.path`, and `user_agent.original`. Setting `VISITOR_ID_SALT` adds `visitor.id`,
and unique visitors is then a `COUNT_DISTINCT` over it:

```
COUNT_DISTINCT(visitor.id)  — optionally GROUP BY http.route
```

`visitor.id` is a salted hash of the client address, the User-Agent, and the
UTC date. Two consequences worth knowing before you trust the number:

- **It rotates at midnight UTC.** The same person is one visitor within a day
  and a new one tomorrow, so the metric is *daily* uniques. Summing across days
  double-counts returning visitors.
- **It approximates.** Visitors sharing one NAT or corporate egress address
  with the same browser collapse into one; one person on laptop and phone
  counts twice.

The raw client address is deliberately never put on a span — `visitor.id`
answers the counting question without exporting an identifier that carries a
PII retention obligation. Set `VISITOR_ID_SALT` from a Secret and use the same
value across replicas, or each replica derives different IDs for the same
person and the count multiplies. Set `TRUST_FORWARDED_FOR=true` only when the
service is reachable exclusively through a proxy that overwrites the header;
on a directly-reachable service any client can forge unlimited visitors.

### Tracking return visitors

`visitor.id` rotates every midnight UTC by design, so it cannot answer "has
this visitor been here before" or "how many times". Setting
`PERSISTENT_VISITOR_COOKIE=true` adds a second, non-rotating identifier for
that: a random UUID v4 stored in a first-party `visitor_id` cookie
(`HttpOnly`, `SameSite=Lax`, ~1 year lifetime), set on a visitor's first
request and read back on every one after. It is exported as
`visitor.persistent_id`, so return visits are a query over the same span:

```
COUNT_DISTINCT(visitor.persistent_id)                — lifetime uniques
COUNT(visitor.persistent_id) / COUNT_DISTINCT(...)   — average visits per visitor
```

Unlike `visitor.id`, this is genuine state stored in the visitor's browser,
not a same-day-only hash — enable it only when a deployment specifically
wants return-visit tracking, not as a default. The ID itself carries no
information: it is a random value, not derived from the client address,
User-Agent, or anything else, so it cannot be reversed to an identity and
does not by itself constitute PII.

### Visitor location

Setting `GEO_COUNTRY_HEADER` (and optionally the region/city variants) lifts
those request headers onto the span as the OTel geo attributes
`geo.country.iso_code`, `geo.region.iso_code`, and `geo.locality.name`. A
header that is absent or empty leaves the dimension off the span rather than
recording `""` as if it were a place.

The IP-to-location lookup happens **upstream, not here**. Something has to hold
a MaxMind database and refresh it monthly; doing that once at the proxy serves
every service behind it instead of bundling a ~60MB database into this image.
A Traefik MaxMind GeoIP2 plugin is the usual source.

**These headers are only as trustworthy as `X-Forwarded-For`** — a client that
can reach this service directly can set them to any value. Configure them only
when a proxy that overwrites them is the sole route in.

## Private repositories (GitHub App)

To track private repos, set `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, and
`GITHUB_APP_PRIVATE_KEY_FILE`. When all three are present, the dashboard mints
short-lived installation tokens and clones/fetches `https://` repos with them.
Credentials are never attached to non-HTTPS remotes. Local-path repos need no
auth. Clones are ephemeral (under the system temp dir); a restart re-clones and
resumes incrementally from the stored high-water mark with no data loss.

## Development

```bash
go build ./...          # build
go test -race ./...     # test (race detector on)
go vet ./...            # static analysis
```

Lint with the same version and configuration CI enforces — `.golangci.yml` at
the repo root documents which linters are enabled and why the rest are not:

```bash
# renovate: datasource=github-releases depName=golangci/golangci-lint
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
```

It is expected to report zero issues; the `golangci-lint` job in
`.github/workflows/pr-ci.yml` fails a PR that makes it report any.

The web UI is server-rendered Go HTML with a single vanilla-JS file
(`internal/web/static/timeline.js`); there is no frontend build step. Storage is
pure-Go SQLite (`modernc.org/sqlite`), so the binary is static and CGO-free.
</content>
