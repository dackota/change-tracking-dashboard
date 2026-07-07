package web

// trackersTemplate is the page markup for GET /trackers (R5, R12): the
// shared shell (sidebar + header, via the "sidebar"/"header" named
// templates — R6) around a table of configured trackers — one row per
// (repo, file-glob, extractor) tracker — listing its repo, file globs,
// tracked fields, poll cadence, backfill window, and poll-health status
// (last success, last error, next run). An empty Trackers slice (no
// trackers configured, or a degraded config read — R7) renders a single
// full-width empty-state row rather than an empty table body. LastError is
// a raw, verbatim poll-failure string (see pollstatus.TrackerStatus's doc
// comment) and is rendered as plain text only — never wrapped in
// template.HTML — so html/template's default auto-escaping always applies.
const trackersTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Trackers — Change Tracking Dashboard</title>
  <style>
` + shellStyles + `
    .trackers-table { width: 100%; border-collapse: collapse; border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel); max-width: 1200px; }
    .trackers-table thead th { text-align: left; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--oc-muted); font-weight: 600; padding: 0.6rem 0.8rem; border-bottom: 1px solid var(--oc-line); }
    .trackers-table tbody td { padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--oc-line-soft); font-size: 0.85rem; vertical-align: top; }
    .trackers-table tbody tr:last-child td { border-bottom: none; }
    .trackers-repo { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 220px; }
    .trackers-empty-row td { text-align: center; color: var(--oc-muted); font-size: 0.9rem; padding: 1.5rem 1rem; }
    .trackers-last-error { color: var(--oc-danger); word-break: break-word; max-width: 260px; }
  </style>
</head>
<body>
  <div class="app">
    {{template "sidebar" .}}
    <main class="main">
      {{template "header" .}}
      <div class="table-scroll"><table class="trackers-table">
        <thead>
          <tr><th>Repository</th><th>File globs</th><th>Tracked fields</th><th>Poll cadence</th><th>Backfill window</th><th>Last success</th><th>Last error</th><th>Next run</th></tr>
        </thead>
        <tbody id="trackers-list">
          {{if .Trackers}}{{range .Trackers}}<tr class="trackers-row" data-tracker-repo="{{.Repo}}">
            <td class="trackers-repo" title="{{.Repo}}">{{.Repo}}</td>
            <td>{{range .FileGlobs}}<div class="trackers-glob">{{.}}</div>{{end}}</td>
            <td>{{range .TrackedFields}}<div class="trackers-field">{{.}}</div>{{end}}</td>
            <td class="trackers-cadence">{{.PollCadence}}</td>
            <td class="trackers-backfill">{{.BackfillWindow}}</td>
            <td class="trackers-last-success">{{.LastSuccess}}</td>
            <td class="trackers-last-error">{{if .LastError}}{{.LastError}}{{else}}—{{end}}</td>
            <td class="trackers-next-run">{{.NextRun}}</td>
          </tr>
          {{end}}{{else}}<tr class="trackers-empty-row"><td colspan="8">No trackers configured.</td></tr>
          {{end}}
        </tbody>
      </table></div>
    </main>
  </div>
</body>
</html>`
