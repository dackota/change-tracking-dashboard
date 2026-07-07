package web

// feedStyles is the CSS for the changeset feed table (thead/tbody, repo dot,
// commit link, empty-state rows) — shared by every page that renders the
// feed via timeline.js's feed-rendering functions (buildFeedRow/
// buildEmptyRow/renderFeed): the Timeline page (GET /) and the Changes page
// (GET /changes, R2). Extracted once here so the two pages' <style> blocks
// can never drift out of sync with each other.
const feedStyles = `
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
    .feed-repo { font-weight: 600; color: var(--oc-ink); display: inline-block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 160px; vertical-align: middle; }
    .feed-commit { font-family: var(--mono); font-size: 0.8rem; color: var(--oc-accent); text-decoration: none; }
    .feed-commit:hover { text-decoration: underline; }
    .feed-commit-plain { color: var(--oc-muted); }
    .feed-cell-author { color: var(--oc-muted); }
    .feed-empty-row td { text-align: center; color: var(--oc-muted); font-size: 0.9rem; padding: 1.5rem 1rem; }
    .feed-clear-btn { font-size: 0.8rem; padding: 0.3rem 0.8rem; margin-left: 0.6rem; border: 1px solid var(--oc-accent); color: var(--oc-accent); background: #fff; border-radius: 6px; cursor: pointer; }
    .feed-load-more-row td { text-align: center; padding: 0.7rem 1rem; }
    .feed-load-more-btn { font-size: 0.82rem; font-weight: 600; padding: 0.35rem 1rem; border: 1px solid var(--oc-accent); color: var(--oc-accent); background: #fff; border-radius: 6px; cursor: pointer; }
    .feed-load-more-btn:hover:not(:disabled) { background: var(--oc-accent); color: #fff; }
    .feed-load-more-btn:disabled { opacity: 0.6; cursor: default; }
    .feed-load-more-error { color: var(--oc-danger); font-size: 0.82rem; font-weight: 600; margin-left: 0.4rem; }
`

// detailStyles is the CSS for a Changeset's expanded detail panel (per-Change
// rows, kind labels, old/new values) and its embedded Chart diff view
// (summary line, added/removed line counts, color-coded unified-diff hunks)
// — rendered into the #timeline-detail-panel element timeline.js's
// ensureDetailPanel mounts on a feed-row click, on any page that renders the
// feed (Timeline and Changes). Extracted alongside feedStyles for the same
// reason: one authored definition, never duplicated per page.
const detailStyles = `
    /* Detail panel */
    #timeline-detail-panel { margin-top: 1.25rem; }
    .changeset-detail { border: 1px solid var(--oc-line); border-radius: 10px; background: var(--oc-panel); padding: 0.9rem 1rem; margin-bottom: 1rem; }
    .changeset-detail-header { display: flex; align-items: center; gap: 0.7rem; flex-wrap: wrap; padding-bottom: 0.6rem; border-bottom: 1px solid var(--oc-line-soft); margin-bottom: 0.6rem; }
    .changeset-detail-repo { font-weight: 700; display: inline-block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 220px; min-width: 0; vertical-align: bottom; }
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
`
