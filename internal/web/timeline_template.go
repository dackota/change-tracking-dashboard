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
    :root {
      /* option-C palette tokens — the single system for the whole page:
         sidebar, header, KPI tiles, facet controls, timeline track, feed,
         detail view, and Chart diff (R17). */
      --oc-canvas:#f1f5f9; --oc-panel:#fff; --oc-ink:#0f172a; --oc-muted:#64748b;
      --oc-line:#e2e8f0; --oc-line-soft:#f1f5f9; --oc-accent:#2563eb;
      --oc-sidebar:#0f172a; --oc-sidebar-ink:#cbd5e1;
      --oc-danger:#dc3545; --oc-success:#198754;
      --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    }
    * { box-sizing: border-box; }
    body { margin: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; background: var(--oc-canvas); color: var(--oc-ink); }

    /* App shell: persistent sidebar + main content column */
    .app { display: flex; min-height: 100vh; align-items: stretch; }
    .sidebar { flex: 0 0 210px; background: var(--oc-sidebar); color: var(--oc-sidebar-ink); padding: 1.1rem 0.9rem; display: flex; flex-direction: column; gap: 0.3rem; }
    .sidebar-brand { display: flex; align-items: center; gap: 0.55rem; padding: 0.2rem 0.4rem 1rem; font-weight: 650; color: #fff; font-size: 0.95rem; }
    .sidebar-logo { width: 24px; height: 24px; border-radius: 7px; background: linear-gradient(135deg, #2563eb, #06b6d4); display: grid; place-items: center; font-size: 0.85rem; }
    .sidebar-nav { display: flex; flex-direction: column; gap: 0.3rem; }
    .nav-item { font-size: 0.84rem; color: var(--oc-sidebar-ink); padding: 0.5rem 0.6rem; border-radius: 8px; display: flex; align-items: center; gap: 0.55rem; }
    .nav-item-active { background: rgba(37, 99, 235, 0.18); color: #fff; }

    .main { flex: 1 1 auto; min-width: 0; padding: 1.4rem 1.8rem 4rem; }
    .page-header { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 1.2rem; }
    .page-header h1 { margin: 0; font-size: 1.25rem; font-weight: 700; letter-spacing: -0.01em; }
    .page-subtitle { color: var(--oc-muted); font-size: 0.85rem; margin: 0; }
    .page-header .spacer { flex: 1; }
    .page-header .btn { font-size: 0.8rem; background: var(--oc-panel); border: 1px solid var(--oc-line); border-radius: 8px; padding: 0.4rem 0.75rem; cursor: pointer; color: var(--oc-ink); }

    /* KPI tiles */
    .kpis { display: grid; grid-template-columns: repeat(4, 1fr); gap: 0.9rem; margin-bottom: 1.3rem; }
    .kpi-tile { background: var(--oc-panel); border: 1px solid var(--oc-line); border-radius: 12px; padding: 0.9rem 1rem; }
    .kpi-label { font-size: 0.72rem; color: var(--oc-muted); text-transform: uppercase; letter-spacing: 0.05em; font-weight: 600; }
    .kpi-value { display: block; font-size: 1.7rem; font-weight: 750; margin-top: 0.15rem; letter-spacing: -0.02em; }
    .kpi-sub { font-size: 0.74rem; color: var(--oc-muted); margin-top: 0.1rem; }

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

    /* Feed */
    .feed-panel { max-width: 900px; }
    .feed-head { display: flex; align-items: baseline; gap: 0.6rem; margin-bottom: 0.5rem; }
    .feed-head h2 { font-size: 1.1rem; margin: 0; }
    .feed-count { font-size: 0.8rem; color: var(--oc-muted); }
    .feed-table { width: 100%; border-collapse: collapse; border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel); }
    .feed-table thead th { text-align: left; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--oc-muted); font-weight: 600; padding: 0.6rem 0.8rem; border-bottom: 1px solid var(--oc-line); }
    .feed-table tbody td { padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--oc-line-soft); font-size: 0.85rem; }
    .feed-table tbody tr:last-child td { border-bottom: none; }
    .feed-table tbody tr.feed-row { cursor: pointer; }
    .feed-table tbody tr.feed-row:hover { background: #f8fbff; }
    .feed-dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%; margin-right: 0.45rem; vertical-align: middle; }
    .feed-cell-when { font-variant-numeric: tabular-nums; color: #495057; white-space: nowrap; }
    .feed-repo { font-weight: 600; color: var(--oc-ink); }
    .feed-commit { font-family: var(--mono); font-size: 0.8rem; color: var(--oc-accent); text-decoration: none; }
    .feed-commit:hover { text-decoration: underline; }
    .feed-commit-plain { color: var(--oc-muted); }
    .feed-cell-author { color: var(--oc-muted); }
    .feed-empty-row td { text-align: center; color: var(--oc-muted); font-size: 0.9rem; padding: 1.5rem 1rem; }
    .feed-clear-btn { font-size: 0.8rem; padding: 0.3rem 0.8rem; margin-left: 0.6rem; border: 1px solid var(--oc-accent); color: var(--oc-accent); background: #fff; border-radius: 6px; cursor: pointer; }

    /* Detail panel */
    #timeline-detail-panel { margin-top: 1.25rem; }
    .changeset-detail { border: 1px solid var(--oc-line); border-radius: 10px; background: var(--oc-panel); padding: 0.9rem 1rem; margin-bottom: 1rem; }
    .changeset-detail-header { display: flex; align-items: center; gap: 0.7rem; flex-wrap: wrap; padding-bottom: 0.6rem; border-bottom: 1px solid var(--oc-line-soft); margin-bottom: 0.6rem; }
    .changeset-detail-repo { font-weight: 700; }
    .changeset-detail-commit { font-family: var(--mono); font-size: 0.82rem; color: var(--oc-accent); text-decoration: none; }
    .changeset-detail-commit:hover { text-decoration: underline; }
    .changeset-detail-author { color: var(--oc-muted); font-size: 0.85rem; }
    .changeset-detail-committed-at { color: var(--oc-muted); font-size: 0.85rem; font-variant-numeric: tabular-nums; margin-left: auto; }
    .changeset-detail-changes { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.75rem; }
    .change { display: flex; flex-wrap: wrap; align-items: center; gap: 0.4rem; font-size: 0.86rem; }
    .change-label { font-size: 0.68rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.03em; padding: 0.1rem 0.4rem; border-radius: 4px; }
    .change-kind-chart .change-label { background: #e7f1ff; color: #084298; }
    .change-kind-value .change-label { background: var(--oc-line-soft); color: #495057; }
    .change-field { font-weight: 600; }
    .change-old-value, .change-dependency-version-old { font-family: var(--mono); color: var(--oc-danger); }
    .change-new-value, .change-dependency-version-new { font-family: var(--mono); color: var(--oc-success); }
    .change-arrow { color: var(--oc-muted); }
    .change-helm-diff-slot { flex-basis: 100%; margin-top: 0.4rem; font-size: 0.82rem; color: var(--oc-muted); }

    /* Chart diff summary + color-coded hunks */
    .chart-diff-summary { display: flex; gap: 0.6rem; align-items: center; font-size: 0.82rem; margin-bottom: 0.5rem; }
    .chart-diff-manifests-changed { font-weight: 600; color: var(--oc-ink); }
    .chart-diff-lines-added { color: var(--oc-success); font-weight: 700; font-family: var(--mono); }
    .chart-diff-lines-removed { color: var(--oc-danger); font-weight: 700; font-family: var(--mono); }
    .chart-diff-truncated-notice { font-size: 0.78rem; color: #b8860b; }
    .chart-diff-message { font-size: 0.85rem; color: var(--oc-muted); font-style: italic; }
    .diff-hunks { font-family: var(--mono); font-size: 0.78rem; line-height: 1.5; border: 1px solid var(--oc-line); border-radius: 8px; overflow-x: auto; background: var(--oc-panel); max-height: 460px; overflow-y: auto; }
    .diff-line { white-space: pre; padding: 0 0.7rem; }
    .diff-add { background: #e6ffed; color: #04260f; }
    .diff-del { background: #ffeef0; color: #3d0a12; }
    .diff-ctx { color: #868e96; }
    .diff-gap { background: var(--oc-line-soft); color: #868e96; font-style: italic; text-align: center; font-family: system-ui, sans-serif; padding: 0.2rem 0.7rem; border-top: 1px solid #eceff1; border-bottom: 1px solid #eceff1; }
  </style>
</head>
<body>
  <div class="app">
    <aside class="sidebar">
      <div class="sidebar-brand"><span class="sidebar-logo">◆</span> ChangeTrack</div>
      <nav class="sidebar-nav" aria-label="Primary">
        {{range .SidebarNav}}<div class="nav-item{{if .Active}} nav-item-active{{end}}" data-nav="{{.Key}}"{{if .Active}} aria-current="page"{{end}}>{{.Label}}</div>
        {{end}}
      </nav>
    </aside>
    <main class="main">
      <div class="page-header">
        <h1>Timeline</h1>
        <p class="page-subtitle">Change activity across tracked repositories.</p>
        <span class="spacer"></span>
        <button type="button" id="header-reset-zoom" class="btn">Reset zoom</button>
      </div>

      <section class="kpis" aria-label="Headline metrics">
        <div class="kpi-tile" data-kpi="changes" data-value="{{.KPI.Changes}}" data-changesets="{{.KPI.Changesets}}">
          <div class="kpi-label">Changes</div>
          <div class="kpi-value">{{.KPI.Changes}}</div>
          <div class="kpi-sub">across {{.KPI.Changesets}} changesets</div>
        </div>
        <div class="kpi-tile" data-kpi="repositories" data-value="{{.KPI.Repositories}}">
          <div class="kpi-label">Repositories</div>
          <div class="kpi-value">{{.KPI.Repositories}}</div>
          <div class="kpi-sub">tracked</div>
        </div>
        <div class="kpi-tile" data-kpi="last-change" data-value="{{.KPI.LastChangeRelative}}" data-absolute="{{.KPI.LastChangeAbsolute}}">
          <div class="kpi-label">Last change</div>
          <div class="kpi-value">{{.KPI.LastChangeRelative}}</div>
          <div class="kpi-sub">{{.KPI.LastChangeAbsolute}}</div>
        </div>
        <div class="kpi-tile" data-kpi="chart-changes" data-value="{{.KPI.ChartChanges}}" data-value-changes="{{.KPI.ValueChanges}}">
          <div class="kpi-label">Chart bumps</div>
          <div class="kpi-value">{{.KPI.ChartChanges}}</div>
          <div class="kpi-sub">{{.KPI.ValueChanges}} value changes</div>
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
        <table class="feed-table">
          <thead>
            <tr><th>When</th><th>Repository</th><th>Commit</th><th>Author</th><th>Changes</th></tr>
          </thead>
          <tbody id="feed-list"></tbody>
        </table>
      </div>
      <script src="/static/timeline.js"></script>
    </main>
  </div>
</body>
</html>`
