package web

// repositoriesTemplate is the page markup for GET /repositories (R3): the
// shared shell (sidebar + header, via the "sidebar"/"header" named
// templates — R6) around a table of tracked repositories — one row per
// repository with at least one recorded Change — listing its Change count,
// chart-change count, and last-change time. An empty Repositories slice (no
// Changes recorded yet, or a degraded store read — R7) renders a single
// full-width empty-state row rather than an empty table body.
const repositoriesTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Repositories — Change Tracking Dashboard</title>
  <style>
` + shellStyles + `
    .repositories-table { width: 100%; border-collapse: collapse; border: 1px solid var(--oc-line); border-radius: 8px; background: var(--oc-panel); max-width: 900px; }
    .repositories-table thead th { text-align: left; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--oc-muted); font-weight: 600; padding: 0.6rem 0.8rem; border-bottom: 1px solid var(--oc-line); }
    .repositories-table tbody td { padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--oc-line-soft); font-size: 0.85rem; vertical-align: top; }
    .repositories-table tbody tr:last-child td { border-bottom: none; }
    .repositories-repo { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 220px; }
    .repositories-empty-row td { text-align: center; color: var(--oc-muted); font-size: 0.9rem; padding: 1.5rem 1rem; }
  </style>
</head>
<body>
  <div class="app">
    {{template "sidebar" .}}
    <main class="main">
      {{template "header" .}}
      <div class="table-scroll"><table class="repositories-table">
        <thead>
          <tr><th>Repository</th><th>Changes</th><th>Chart changes</th><th>Last change</th></tr>
        </thead>
        <tbody id="repositories-list">
          {{if .Repositories}}{{range .Repositories}}<tr class="repositories-row" data-repository-repo="{{.Repo}}">
            <td class="repositories-repo" title="{{.Repo}}">{{.Repo}}</td>
            <td class="repositories-change-count">{{.ChangeCount}}</td>
            <td class="repositories-chart-changes">{{.ChartChanges}}</td>
            <td class="repositories-last-change" title="{{.LastChangeAbsolute}}">{{.LastChangeRelative}}</td>
          </tr>
          {{end}}{{else}}<tr class="repositories-empty-row"><td colspan="4">No repositories tracked yet.</td></tr>
          {{end}}
        </tbody>
      </table></div>
    </main>
  </div>
</body>
</html>`
