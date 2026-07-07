package web

// timelineTemplate is the observability-shell markup for GET /: a persistent
// sidebar (R1), a header with the global Reset zoom action (R2), a row of
// server-computed KPI tiles (R3-R7), the pre-existing facet controls and
// timeline track (unchanged), and the Changes feed as a table (thead: When,
// Repository, Commit, Author, Changes). <tbody id="feed-list"> keeps the
// exact id timeline.js has always wired; timeline.js (feed-table slice)
// fills it with real <tr>/<td> rows and renders the loading/nothing-
// recorded-yet/nothing-in-window-or-filters states as full-width in-table
// rows — there is no longer a separate skeleton placeholder for the empty
// state.
//
// <div id="facet-chips"> is an empty mount point: timeline.js's
// renderFacetChips fills it with one removable chip per active facet/value
// pair (R21), derived from the pure facetChips(facetState) mapping (R24).
// #facet-clear (now labeled "Clear all filters", R22) resets all facet
// state and re-syncs the chips in lockstep. Include vs exclude chips are
// visually distinct via the .facet-chip-include/.facet-chip-exclude CSS
// classes below (R23) — the same accent/danger colors already used for the
// per-value pills.
//
// The option-C palette (blue accent, slate sidebar, light-slate canvas) is
// expressed as the --oc-* CSS custom properties below and used across the
// entire page — shell, facet controls, timeline track, feed, detail view,
// and Chart diff (R17). The pre-existing --ink/--muted/--line/--line-soft/
// --blue/--red/--surface/--bg tokens have been retired; every rule that used
// to read one of them now reads the equivalent --oc-* token instead, so
// there is exactly one palette on the page, not two parallel ones.
const timelineTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Change Tracking Dashboard</title>
  <style>
` + shellStyles + `
    /* KPI tiles */
    .kpis { display: grid; grid-template-columns: repeat(5, 1fr); gap: 0.9rem; margin-bottom: 1.3rem; }
    .kpi-tile { background: var(--oc-panel); border: 1px solid var(--oc-line); border-radius: 12px; padding: 0.9rem 1rem; }
    .kpi-label { font-size: 0.72rem; color: var(--oc-muted); text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; }
    .kpi-value { display: block; font-size: 1.7rem; font-weight: 750; margin-top: 0.15rem; letter-spacing: -0.02em; }
    .kpi-sub { font-size: 0.74rem; color: var(--oc-muted); margin-top: 0.1rem; }
    /* KPI reflow (R16): 5 -> 2 -> 1 columns as the viewport narrows, so no
       tile ever clips at mobile/tablet widths. */
    @media (max-width: 860px) {
      .kpis { grid-template-columns: repeat(2, 1fr); }
    }
    @media (max-width: 600px) {
      .kpis { grid-template-columns: 1fr; }
    }

    /* Facet dropdowns */
    .facet-bar { display: flex; align-items: center; gap: 0.6rem; flex-wrap: wrap; margin-bottom: 1rem; }
    .facet-bar-label { font-size: 0.8rem; font-weight: 700; color: var(--oc-muted); text-transform: uppercase; letter-spacing: 0.04em; }
    .facet-controls.facet-dropdowns { display: flex; flex-wrap: wrap; gap: 0.5rem; }
    /* Progressive-enhancement fallback: raw controls before JS builds dropdowns */
    .facet-control { font-size: 0.8rem; padding: 0.25rem 0.6rem; border: 1px solid #ced4da; border-radius: 999px; background: #fff; cursor: pointer; }
    .facet-control[data-state="include"] { background: var(--oc-accent); border-color: var(--oc-accent); color: #fff; }
    .facet-control[data-state="exclude"] { background: var(--oc-danger); border-color: var(--oc-danger); color: #fff; }
    details.facet-dd { border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel); font-size: 0.83rem; }
    details.facet-dd > summary { list-style: none; cursor: pointer; padding: 0.35rem 0.7rem; display: flex; align-items: center; gap: 0.4rem; font-weight: 600; color: #343a40; user-select: none; }
    details.facet-dd > summary::-webkit-details-marker { display: none; }
    details.facet-dd > summary::after { content: "▾"; color: var(--oc-muted); font-size: 0.75rem; }
    details.facet-dd[open] > summary { border-bottom: 1px solid var(--oc-line-soft); }
    .facet-dd-name { text-transform: capitalize; }
    .facet-dd-badge { background: var(--oc-accent); color: #fff; font-size: 0.68rem; font-weight: 700; border-radius: 999px; min-width: 1.1rem; text-align: center; padding: 0 0.35rem; }
    .facet-dd-body { padding: 0.4rem 0.5rem; display: flex; flex-direction: column; gap: 0.3rem; max-height: 260px; overflow-y: auto; min-width: 190px; }
    .facet-row { display: flex; align-items: center; gap: 0.4rem; }
    .facet-pill { flex: 1 1 auto; text-align: left; font-size: 0.8rem; padding: 0.2rem 0.55rem; border: 1px solid #ced4da; border-radius: 6px; background: #fff; cursor: pointer; color: var(--oc-ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .facet-pill[data-state="include"] { background: var(--oc-accent); border-color: var(--oc-accent); color: #fff; }
    .facet-pill[data-state="exclude"] { background: var(--oc-danger); border-color: var(--oc-danger); color: #fff; }
    .facet-pill[data-state="include"]::before { content: "✓ "; }
    .facet-pill[data-state="exclude"]::before { content: "✕ "; }
    .facet-only { font-size: 0.7rem; color: var(--oc-accent); background: none; border: 1px solid transparent; border-radius: 5px; cursor: pointer; padding: 0.15rem 0.35rem; }
    .facet-only:hover { border-color: var(--oc-accent); }
    .facet-clear { font-size: 0.78rem; font-weight: 600; padding: 0.3rem 0.7rem; border: 1px solid var(--oc-line); background: #fff; border-radius: 6px; cursor: pointer; color: #495057; }
    .facet-clear:hover { border-color: var(--oc-danger); color: var(--oc-danger); }
    .facet-clear[hidden] { display: none; }

    /* Active-filter chips (R21/R23): one removable chip per facet/value pair,
       rendered by timeline.js's renderFacetChips from the pure
       facetChips(facetState) mapping. Include vs exclude get distinct
       accent/danger colors so mode is visually obvious without reading text. */
    .facet-chips { display: flex; flex-wrap: wrap; gap: 0.4rem; }
    .facet-chip { display: inline-flex; align-items: center; gap: 0.3rem; font-size: 0.78rem; font-weight: 600; padding: 0.2rem 0.3rem 0.2rem 0.6rem; border-radius: 999px; border: 1px solid transparent; }
    .facet-chip-include { background: rgba(37, 99, 235, 0.12); border-color: var(--oc-accent); color: var(--oc-accent); }
    .facet-chip-exclude { background: rgba(220, 53, 69, 0.10); border-color: var(--oc-danger); color: var(--oc-danger); }
    .facet-chip-label { white-space: nowrap; }
    .facet-chip-remove { font-size: 0.85rem; line-height: 1; border: none; background: none; cursor: pointer; color: inherit; padding: 0 0.15rem; border-radius: 50%; }
    .facet-chip-remove:hover { background: rgba(0, 0, 0, 0.08); }

    /* Timeline controls + track */
    #timeline-root { background: var(--oc-panel); border: 1px solid var(--oc-line); border-radius: 10px; padding: 0.75rem 0.9rem; margin-bottom: 1.3rem; }
    .timeline-controls { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.6rem; flex-wrap: wrap; }
    .range-toggle { font-size: 0.8rem; font-weight: 600; padding: 0.3rem 0.7rem; border: 1px solid var(--oc-accent); color: var(--oc-accent); background: #fff; border-radius: 6px; cursor: pointer; }
    .range-toggle[data-active="true"] { background: var(--oc-accent); color: #fff; }
    .range-label { font-size: 0.8rem; font-weight: 600; color: #495057; display: flex; align-items: center; gap: 0.35rem; }
    .range-label input { font-size: 0.8rem; padding: 0.2rem 0.4rem; border: 1px solid #ced4da; border-radius: 4px; }
    .range-clear { font-size: 0.78rem; padding: 0.25rem 0.6rem; border: 1px solid var(--oc-line); background: #fff; border-radius: 6px; cursor: pointer; color: #495057; }
    .range-clear:disabled { opacity: 0.45; cursor: default; }
    .timeline-hint { font-size: 0.76rem; color: var(--oc-muted); font-style: italic; }
    .timeline-svg { display: block; max-width: 100%; }

` + feedStyles + detailStyles + `
  </style>
</head>
<body>
  <div class="app">
    {{template "sidebar" .}}
    <main class="main">
      {{template "header" .}}

      <section class="kpis" aria-label="Headline metrics">
        <div class="kpi-tile" data-kpi="changes" data-value="{{.KPI.Changes}}" data-changesets="{{.KPI.Changesets}}" title="Change: a detected delta in a tracked field between two consecutive commits (old to new, with commit SHA, author, timestamp), diffed by key. A Changeset is all the Changes produced by a single commit.">
          <div class="kpi-label">Changes</div>
          <div class="kpi-value">{{.KPI.Changes}}</div>
          <div class="kpi-sub">across {{.KPI.Changesets}} changesets</div>
        </div>
        <div class="kpi-tile" data-kpi="repositories" data-value="{{.KPI.Repositories}}" title="Repositories: the number of distinct repositories with at least one tracked Change in the retained history shown here.">
          <div class="kpi-label">Repositories</div>
          <div class="kpi-value">{{.KPI.Repositories}}</div>
          <div class="kpi-sub">tracked</div>
        </div>
        <div class="kpi-tile" data-kpi="last-change" data-value="{{.KPI.LastChangeRelative}}" data-absolute="{{.KPI.LastChangeAbsolute}}" title="Last change: the commit timestamp of the most recent Changeset, the latest commit that produced at least one Change.">
          <div class="kpi-label">Last change</div>
          <div class="kpi-value">{{.KPI.LastChangeRelative}}</div>
          <div class="kpi-sub">{{.KPI.LastChangeAbsolute}}</div>
        </div>
        <div class="kpi-tile" data-kpi="chart-changes" data-value="{{.KPI.ChartChanges}}" title="ChartChanges: Changes whose kind is a chart-version bump, reflected as a Chart diff (the rendered-manifest delta between the old and new chart version, same tenant/values).">
          <div class="kpi-label">Chart-version bumps</div>
          <div class="kpi-value">{{.KPI.ChartChanges}}</div>
          <div class="kpi-sub">of {{.KPI.Changes}} total changes</div>
        </div>
        <div class="kpi-tile" data-kpi="value-changes" data-value="{{.KPI.ValueChanges}}" title="ValueChanges: edits to a tracked field that are not chart-version bumps (Changes minus ChartChanges), e.g. a dependency version, a subchart version, or an image tag.">
          <div class="kpi-label">Value changes</div>
          <div class="kpi-value">{{.KPI.ValueChanges}}</div>
          <div class="kpi-sub">of {{.KPI.Changes}} total changes</div>
        </div>
      </section>

      <div class="facet-bar">
        <span class="facet-bar-label">Filter</span>
        <div id="facet-controls" class="facet-controls">
          {{range .FacetControls}}<button type="button" class="facet-control" data-facet="{{.Facet}}" data-value="{{.Value}}" data-state="off">{{.Facet}}: {{.Value}}</button>
          {{end}}
        </div>
        <div id="facet-chips" class="facet-chips"></div>
        <button type="button" id="facet-clear" class="facet-clear" hidden>Clear all filters</button>
      </div>
      <div id="timeline-root"></div>
      <div id="feed-panel" class="feed-panel">
        <div class="feed-head">
          <h2 id="feed-title">Changes</h2>
          <span id="feed-count" class="feed-count"></span>
        </div>
        <div class="table-scroll"><table class="feed-table">
          <thead>
            <tr><th>When</th><th>Repository</th><th>Commit</th><th>Author</th><th>Changes</th></tr>
          </thead>
          <tbody id="feed-list"></tbody>
        </table></div>
      </div>
      <script src="/static/timeline.js"></script>
    </main>
  </div>
</body>
</html>`
