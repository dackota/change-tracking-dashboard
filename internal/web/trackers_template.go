package web

// trackersTemplate is the page markup for GET /trackers (R5): the shared
// shell (sidebar + header, via the "sidebar"/"header" named templates —
// R6) around a table of configured trackers — one row per (repo,
// file-glob, extractor) tracker — listing its repo, file globs, tracked
// fields, poll cadence, and backfill window. An empty Trackers slice (no
// trackers configured, or a degraded config read — R7) renders a single
// full-width empty-state row rather than an empty table body.
const trackersTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Trackers — Change Tracking Dashboard</title>
  <style>
` + shellStyles + `
    .trackers-table { width: 100%; border-collapse: collapse; border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel); max-width: 1000px; }
    .trackers-table thead th { text-align: left; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--oc-muted); font-weight: 600; padding: 0.6rem 0.8rem; border-bottom: 1px solid var(--oc-line); }
    .trackers-table tbody td { padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--oc-line-soft); font-size: 0.85rem; vertical-align: top; }
    .trackers-table tbody tr:last-child td { border-bottom: none; }
    .trackers-repo { font-weight: 600; }
    .trackers-empty-row td { text-align: center; color: var(--oc-muted); font-size: 0.9rem; padding: 1.5rem 1rem; }
  </style>
</head>
<body>
  <div class="app">
    {{template "sidebar" .}}
    <main class="main">
      {{template "header" .}}
      <table class="trackers-table">
        <thead>
          <tr><th>Repository</th><th>File globs</th><th>Tracked fields</th><th>Poll cadence</th><th>Backfill window</th></tr>
        </thead>
        <tbody id="trackers-list">
          {{if .Trackers}}{{range .Trackers}}<tr class="trackers-row" data-tracker-repo="{{.Repo}}">
            <td class="trackers-repo">{{.Repo}}</td>
            <td>{{range .FileGlobs}}<div class="trackers-glob">{{.}}</div>{{end}}</td>
            <td>{{range .TrackedFields}}<div class="trackers-field">{{.}}</div>{{end}}</td>
            <td class="trackers-cadence">{{.PollCadence}}</td>
            <td class="trackers-backfill">{{.BackfillWindow}}</td>
          </tr>
          {{end}}{{else}}<tr class="trackers-empty-row"><td colspan="5">No trackers configured.</td></tr>
          {{end}}
        </tbody>
      </table>
    </main>
  </div>
</body>
</html>`
