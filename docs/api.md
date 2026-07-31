# HTTP API reference

Complete reference for the change-tracking dashboard's HTTP surface. The
[README](../README.md#http-endpoints) covers the same endpoints narratively,
with worked `curl` examples and the reasoning behind the design decisions; this
document is the field-by-field contract.

The API is **unauthenticated and read-only**. Every route is `GET`; nothing here
mutates state. Deploy it behind whatever authentication your environment
provides.

## Routes

| Path                                  | Representation      | Purpose                                        |
| ------------------------------------- | ------------------- | ---------------------------------------------- |
| `/`                                   | HTML                | Timeline: KPIs, timeline chart, change feed.   |
| `/changes`                            | HTML                | The change feed on its own.                    |
| `/repositories`                       | HTML                | Per-repository rollups.                        |
| `/trackers`                           | HTML                | Configured trackers and per-tracker poll health. |
| `/healthz`                            | `text/plain`        | Liveness check.                                |
| `/static/*`                           | assets              | Embedded CSS/JS.                               |
| `/api/changesets`                     | JSON                | Changeset list, cursor-paginated.              |
| `/api/changesets/detail`              | JSON or HTML        | One changeset in full.                         |
| `/api/changesets/detail/chart-diff`   | JSON or HTML        | Helm manifest-level blast radius.              |
| `/api/changesets/detail/plan-diff`    | JSON or HTML        | Terraform resource-level blast radius.         |

## Content negotiation

`/api/changesets` is always JSON. The three `detail*` routes serve two
representations, chosen by the `Accept` header:

| `Accept` contains                          | Response                    |
| ------------------------------------------ | --------------------------- |
| `application/json` (named explicitly)      | JSON                        |
| anything else — `*/*`, `text/html`, absent | HTML fragment (the default) |

A wildcard does **not** opt in. The dashboard's own UI fetches these routes with
`XMLHttpRequest` and no `Accept` header, so the browser sends `*/*` and must
keep receiving HTML fragments. Media-type parameters (`;q=0.9`) are ignored —
this is a presence check, not q-value ranking. If you want JSON, name it.

The negotiated representation applies to **error** responses too, so a client
parses success and failure through one code path.

## Errors

| Status | When                                                                   |
| ------ | ---------------------------------------------------------------------- |
| `400`  | Missing required param, malformed `since`/`asOf`/`limit`/`cursor`, unrecognized `impact` or `risk` value. |
| `404`  | Unknown changeset (`detail*` routes only).                             |
| `500`  | Internal failure (store error, resolver error).                        |

Bodies are generic and **never echo request values back**:

```json
{ "error": "not found" }
```

The three messages are `"bad request"`, `"not found"`, and
`"internal server error"`. When HTML is negotiated the body is plain text
carrying the same message. Underlying detail — SQLite errors, cursor bytes, git
and Helm/HCL internals — stays in the server logs.

`404` deliberately reveals nothing about *why*: an unknown repo, an unknown
commit, and (on the diff routes) a known commit with a non-matching `path` are
all indistinguishable, in both representations. The routes cannot be used to
enumerate what has been ingested.

## Security headers

Every response, including errors, carries:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'
```

---

## `GET /api/changesets`

Changesets newest-first, cursor-paginated.

### Query parameters

| Param    | Type              | Default | Description                                                     |
| -------- | ----------------- | ------- | --------------------------------------------------------------- |
| `since`  | RFC3339           | none    | **Inclusive** lower bound on commit time.                        |
| `asOf`   | RFC3339           | now     | **Exclusive** upper bound on commit time.                        |
| `repo`   | string            | all     | Restrict to one tracked repository. Empty is a no-op.            |
| `cursor` | opaque            | —       | `nextCursor` from a previous response. Omit for the first page.  |
| `limit`  | int               | `50`    | Page size. Must be positive; values above `100` clamp to `100`.  |
| `impact` | enum, repeatable  | none    | `major`, `minor`, `patch`, `downgrade`, `other`.                 |
| `risk`   | slug, repeatable  | none    | `replace-destroy`, `security`, `cost-tripwire`, `major-version-bump`. |
| *facets* | string, repeatable| none    | Any configured facet name. `-value` excludes; anything else includes. |

`since` and `asOf` form a half-open window `[since, asOf)`, so feeding one
request's `asOf` back as the next request's `since` tiles the timeline exactly
once — no gaps, no duplicates. A `since` at or after `asOf` is an empty window:
`200` with an empty list, not an error. A malformed timestamp is `400`.

Repeated values within one param OR together; different params AND together
(`impact` AND `risk` AND `repo` AND facets).

**Unknown param names are silently ignored** if they aren't a configured facet
name — the facet vocabulary is open-ended and data-derived, so rejecting typos
there is not possible. `impact` and `risk` are closed vocabularies and *do*
reject unknown values with `400`, so `impact=majr` can't quietly return the
whole unfiltered feed. The reserved names `asOf`, `cursor`, `impact`, `limit`,
`repo`, `risk`, and `since` are never treated as facets, even if a stored change
carries a facet of the same name.

### Pagination

Follow `nextCursor` until it comes back **empty**. A non-empty cursor is the
only signal that more results exist.

`impact` and `risk` are applied after changesets are assembled, under a
per-request bound on how many commits are examined, so a selective filter can
return a **short page that is not the last page**. Never infer completion from a
page shorter than `limit`. An invalid `cursor` is `400`.

### Response

```json
{
  "changesets": [
    {
      "repo": "infra-repo",
      "commitSha": "abc123",
      "author": "someone@example.com",
      "committedAt": "2026-07-29T14:03:11.123456789Z",
      "subject": "bump app chart to 2.0.0",
      "issueRefs": ["PROJ-421"],
      "risk": ["major version bump"],
      "impact": "major",
      "changes": [
        {
          "field": "spec.chart.version",
          "key": "app",
          "changeType": "modified",
          "oldValue": "1.9.0",
          "newValue": "2.0.0",
          "kind": "chart"
        }
      ]
    }
  ],
  "nextCursor": "..."
}
```

#### Changeset fields

| Field         | Type       | Notes                                                          |
| ------------- | ---------- | -------------------------------------------------------------- |
| `repo`        | string     | Tracked repository name.                                        |
| `commitSha`   | string     | Commit the changeset was derived from.                          |
| `author`      | string     |                                                                 |
| `committedAt` | string     | RFC3339 with nanoseconds, always UTC.                           |
| `subject`     | string     | Commit message's first line. **Omitted when empty** (rows ingested before subjects were captured) — fall back to `commitSha`. |
| `issueRefs`   | string[]   | Extracted issue references. Omitted when empty.                 |
| `changes`     | object[]   | Always present; see below.                                      |
| `risk`        | string[]   | Always present, `[]` when empty, never `null`.                  |
| `impact`      | string     | Always exactly one tier, never empty.                           |

`risk` and `impact` are **query-time projections**, not stored data: they are
recomputed on every read from the changeset's contents. `risk` in particular
depends on the operator's configured rules, so the same changeset can classify
differently across deployments.

#### Change fields

| Field        | Type   | Notes                                                    |
| ------------ | ------ | -------------------------------------------------------- |
| `field`      | string | Extracted field path.                                     |
| `key`        | string | Sub-identity within the field. Omitted when absent.       |
| `changeType` | enum   | `added`, `removed`, `modified`.                           |
| `oldValue`   | string | Omitted for `added`.                                      |
| `newValue`   | string | Omitted for `removed`.                                    |
| `kind`       | enum   | `chart`, `value`, `provider`, `module`, `resource`, `variable`. |

`kind` is derived from the source file's role: `chart` from `Chart.yaml`,
`provider`/`module`/`resource`/`variable` from Terraform/OpenTofu sources
(`.tf`, `.tofu`, `.terraform.lock.hcl`), and `value` from everything else.

### Risk slugs

The request vocabulary uses stable slugs; the response's `risk[]` carries
display values. Only slugs are accepted on the wire.

| Slug                 | Display value in `risk[]` |
| -------------------- | ------------------------- |
| `replace-destroy`    | `replace/destroy`         |
| `security`           | `security`                |
| `cost-tripwire`      | `cost tripwire`           |
| `major-version-bump` | `major version bump`      |

A changeset matches when its risk set **intersects** the requested set; a
changeset with no risk classes never matches a non-empty `risk` filter.

Every slug is always accepted, even when no configured rule can produce it — the
default rule set does not produce `major-version-bump`, so filtering on it
returns an empty list that looks identical to "nothing matched". The server logs
a `WARN` naming the class and the remedy whenever a `risk` filter names an
unreachable class.

---

## `GET /api/changesets/detail`

One changeset in full, identified by **`repo` and `commitSha` (both required)**.

The JSON body is a single changeset object in exactly the shape the list
endpoint emits — same field names, same `risk[]`/`impact` projections — so one
parser handles both endpoints. The HTML representation is a rendered fragment
for the UI's detail slot.

`400` when either param is missing, `404` for an unknown changeset.

---

## `GET /api/changesets/detail/chart-diff`

The manifest-level blast radius of a Helm chart version bump: what actually
changed in the rendered Kubernetes manifests, not just the version string.

**Required params:** `repo`, `commitSha`, `path` (the chart's directory, as a
forward-slash git path — e.g. `workloads/app`).

`path` is an authorization input, not just a selector: it must be the directory
of one of *that changeset's own* chart-kind changes. A mismatch returns the same
opaque `404` as an unknown changeset.

### Outcome kinds

| `kind`             | Meaning                                                             |
| ------------------ | -------------------------------------------------------------------- |
| `ok`               | The diff computed; `diff` is present.                                |
| `no-prior-version` | Root commit — no "old" side to diff against.                         |
| `unavailable`      | A chart dependency has no vendored artifact; this service never pulls from a registry. |
| `could-not-render` | The chart could not be rendered (malformed content, or any other failure). |
| `exceeded-limits`  | The render hit a configured timeout or resource ceiling.             |

Every outcome returns **`200`** — `kind` is the classification, not an error.
The `400`/`404`/`500` codes signal a bad *request*, not a failed render.

### Response

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

| Field                     | Type    | Notes                                                    |
| ------------------------- | ------- | -------------------------------------------------------- |
| `kind`                    | enum    | Always present.                                           |
| `diff.unified`            | string  | Unified diff text.                                        |
| `diff.truncated`          | bool    | `true` when `unified` was cut short by the size ceiling.  |
| `diff.summary.*`          | int     | **True totals, computed before truncation.**              |

**Check `truncated` before presenting a diff as complete.** The summary counts
stay honest either way.

Every non-`ok` outcome is the `kind` and nothing else — `diff` is omitted
entirely. No error strings, Helm output, or git internals reach the wire:

```json
{ "kind": "could-not-render" }
```

---

## `GET /api/changesets/detail/plan-diff`

The Terraform counterpart: the **static** resource-level blast radius of a
change, computed from the materialized subtree — no `terraform plan`, no
provider credentials, no state access.

**Required params:** `repo`, `commitSha`, `path`. Same `Accept` rule, same
status codes, same opaque `404`, and the same path-scoped authorization gate
(the path must carry one of that changeset's own Terraform-kind changes).

### Outcome kinds

`ok`, `no-prior-version`, `could-not-render`, `exceeded-limits` — the chart-diff
vocabulary minus `unavailable`. A Terraform resource block is always statically
resolvable from the subtree, so there is no registry fetch to decline.

### Response

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

`diff` is the same `unifiedDiff` shape the chart-diff endpoint uses, so both
endpoints describe a unified diff identically.

#### `summary` — aggregate resource counts

| Field      | Notes                                                                   |
| ---------- | ------------------------------------------------------------------------ |
| `added`    |                                                                          |
| `removed`  |                                                                          |
| `changed`  |                                                                          |
| `replaced` | How many of `removed` + `changed` force replacement — a **subset**, not a separate category. |

`added + removed + changed` is the total resource count; adding `replaced` would
double-count. The aggregate is served so a one-line summary ("2 resources force
replacement") needs no client-side computation over `resources`.

#### `resources` — per-resource deltas

| Field               | Type | Notes                                                         |
| ------------------- | ---- | -------------------------------------------------------------- |
| `type`              | string | Resource type, e.g. `oci_core_instance`. With `name`, the HCL address. |
| `name`              | string |                                                                |
| `kind`              | enum | `added`, `removed`, `changed`.                                   |
| `forcesReplacement` | bool | `true` for any removal (always destructive) or a change touching a force-replacement attribute; always `false` for an addition. |

`resources` is emitted in a deterministic `(type, name)` sorted order, so output
is stable across requests and two responses can be diffed meaningfully.

As with chart-diff, a non-`ok` outcome carries the `kind` alone — `diff`,
`summary`, and `resources` are all omitted.

---

## `GET /healthz`

Always `200` with the body `ok`. It is a **liveness** probe only: it never
touches the store, config watcher, or poll status, so it answers "is this
process still serving HTTP", not "is everything downstream healthy". There is no
readiness endpoint.

---

## Polling recipe

Incremental consumption of the feed, using the half-open window and cursors:

```bash
SINCE=2026-07-29T00:00:00Z
ASOF=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CURSOR=""

while :; do
  RESP=$(curl -sG "$BASE/api/changesets" \
    --data-urlencode "since=$SINCE" \
    --data-urlencode "asOf=$ASOF" \
    --data-urlencode "cursor=$CURSOR")
  echo "$RESP" | jq -c '.changesets[]'
  CURSOR=$(echo "$RESP" | jq -r '.nextCursor')
  [ -n "$CURSOR" ] || break
done

# next run: SINCE=$ASOF
```

Carrying `asOf` forward as the next run's `since` is what makes consecutive runs
tile the timeline exactly once.
