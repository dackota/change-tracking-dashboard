package web

// changesTemplate is the page markup for GET /changes (R2): the shared shell
// (sidebar + header, via the "sidebar"/"header" named templates — R6) around
// the changeset feed as a full-page table (thead: When, Repository, Commit,
// Author, Changes, Risk — R24). <tbody id="feed-list"> keeps the exact id timeline.js's
// feed-rendering functions (buildFeedRow/buildEmptyRow/renderFeed) already
// wire up — this page loads the same first-party <script src="/static/
// timeline.js"> the Timeline page does, so the feed here is the Timeline
// page's feed rendering, reused unchanged, not reimplemented.
//
// Deliberately omitted, unlike the Timeline page: the #timeline-root
// zoomable track, its From/To/Reset-zoom controls, the facet dropdowns, and
// the KPI tiles — "browse change history without the timeline in the way"
// is this page's whole point (R2's user story). timeline.js's init() only
// builds the track/facet chrome when #timeline-root is present; omitting it
// here leaves the feed (and a clicked row's detail panel, mounted into
// #feed-panel) fully functional with nothing else on the page.
const changesTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Changes — Change Tracking Dashboard</title>
  <style>
` + shellStyles + feedStyles + detailStyles + `
  </style>
</head>
<body>
  <div class="app">
    {{template "sidebar" .}}
    <main class="main">
      {{template "header" .}}
      <div id="feed-panel" class="feed-panel">
        <div class="feed-head">
          <h2 id="feed-title">Changes</h2>
          <span id="feed-count" class="feed-count"></span>
        </div>
        <div class="table-scroll"><table class="feed-table">
          <thead>
            <tr><th>When</th><th>Repository</th><th>Commit</th><th>Author</th><th>Changes</th><th>Risk</th></tr>
          </thead>
          <tbody id="feed-list"></tbody>
        </table></div>
      </div>
      <script src="/static/timeline.js"></script>
    </main>
  </div>
</body>
</html>`
