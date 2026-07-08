// timeline.js — vendored, embedded (go:embed) client-side rendering for the
// Change Tracking Dashboard. This is the dashboard's only client-side
// script; it stays thin, and it is shared, unmodified, by every page that
// renders the changeset feed: the Timeline page (GET /, the zoomable track
// plus the feed) and the Changes page (GET /changes, R2 — the same feed as a
// standalone full page, "without the timeline in the way"). init() only
// builds the timeline track, its controls, and the facet dropdowns when
// #timeline-root is present on the page; the feed itself (and a clicked
// row's detail panel, which mounts into #timeline-root when present or
// #feed-panel otherwise) initializes unconditionally, so a track-less page
// still gets the exact same feed rendering — never a reimplementation. All
// querying, grouping, classification, facet filtering, and per-kind (chart
// vs value) detail rendering stay server-side (store/changeset/filter and
// the /api/changesets* endpoints). This file:
//   - fetches Changesets from /api/changesets (backdrop, facet-filtered)
//   - renders one flag per Changeset on a single dated time track (Timeline
//     page only)
//   - groups the server-rendered facet controls into per-facet dropdowns, each
//     value cycling off -> include -> exclude, plus an "only" shortcut, plus a
//     single "Clear all filters" reset
//   - mirrors every active facet/value pair as a removable chip (facet,
//     value, and include/exclude mode) via the pure facetChips(facetState)
//     mapping — the chip model driving what renderFacetChips puts on screen
//   - uses the visible window AS the feed filter (Datadog-style): drag on the
//     track to zoom into a range (the feed follows), scroll to zoom, shift-drag
//     to pan, "Reset zoom" to return to the full span (both the track's own
//     embedded control and the observability shell's header action trigger
//     it); the From/To inputs mirror and drive the window
//   - re-clusters flags on every render so zooming in splits a stacked marker
//     apart; clicking a cluster zooms into its own span to expand it
//   - renders the "Changes" feed as a table (<tr>/<td> rows in the
//     server-rendered <table>'s <tbody>) for the visible window, with
//     explicit loading / empty states rendered as full-width in-table rows,
//     short repo names, GitHub commit links and day/time stamps
//   - on a flag (or feed row) click, fetches the server-rendered detail HTML
//     and, per chart-kind Change, its chart diff — re-rendered client-side as a
//     collapsed, color-coded (red/green) hunk view
//
// Security posture is unchanged: the only innerHTML/insertAdjacentHTML writes
// are the server-rendered, already-escaped detail panel and chart-diff slots.
// Every client-built string is assigned via textContent.

(function () {
  'use strict';

  var API_PATH = '/api/changesets';
  var DETAIL_API_PATH = '/api/changesets/detail';
  var CHART_DIFF_API_PATH = '/api/changesets/detail/chart-diff';
  var PLAN_DIFF_API_PATH = '/api/changesets/detail/plan-diff';

  var REPO_COLORS = {
    'application-config': '#0d6efd',
    'infrastructure-config': '#fd7e14'
  };
  var DEFAULT_COLOR = '#6c757d';

  // Flags within this many pixels collapse into one counted cluster. Kept tight
  // so that zooming in separates near-simultaneous changes quickly.
  var CLUSTER_PIXEL_RADIUS = 7;

  var MIN_WINDOW_MS = 5 * 60 * 1000; // 5 min — allow tight zoom so stacks split
  var MAX_WINDOW_MS = 5 * 365 * 24 * 60 * 60 * 1000; // ~5 years
  var DEFAULT_WINDOW_MS = 14 * 24 * 60 * 60 * 1000; // 2 weeks (empty fallback)
  var ZOOM_STEP = 1.4;
  var MIN_DRAG_PX = 4; // below this a drag is treated as a click, not a select

  var BACKDROP_LIMIT = 100;
  var FEED_COLUMN_COUNT = 5; // When, Repository, Commit, Author, Changes — matches the thead
  var DIFF_CONTEXT = 3;
  var AXIS_TICKS = 6;
  var MS_PER_DAY = 24 * 60 * 60 * 1000;
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  var FACET_STATE_CYCLE = ['off', 'include', 'exclude'];

  var root = null;
  var svg = null;
  var trackWidth = 900;
  var trackHeight = 120;
  var trackMidY = 55;

  // The visible window [windowStart(), windowEnd] is the single source of truth
  // for both what the track shows and what the feed lists.
  var state = {
    windowEnd: Date.now(),
    windowMs: DEFAULT_WINDOW_MS,
    changesets: [],
    loaded: false,
    hasFitWindow: false,
    nextCursor: '',     // R25: the server's opaque cursor for the next page beyond what's loaded, or '' when there is none
    loadingMore: false, // R25: true while a loadMore() fetch is in flight, guarding a double-click from racing two fetches
    loadMoreError: false, // true when the most recent loadMore() fetch failed (non-200/parse/network error) — distinct from a real empty nextCursor, so a transient hiccup never silently removes the Load more row
    backdropError: false // true when the most recent loadBackdrop() fetch (fired on initial load and on every filter/repo-scope change via onFilterChanged) failed (non-200/parse/network error) — distinct from a real, successfully-fetched empty page, so a fetch failure is never rendered as "No changes recorded yet"; mirrors loadMoreError's own distinction for the "Load more" affordance
  };

  var facetState = {};   // facetState[facet][value] = 'include' | 'exclude'
  var facetValues = {};  // facet -> [values]
  var pillEls = {};      // facet -> value -> pill element
  var badgeEls = {};     // facet -> badge element

  // repoState is the single chosen repo scope (R26) — "" means "All
  // repositories" (no scope). Unlike facetState, this is not tri-state: a
  // repo is a single scoping choice, not a per-value include/exclude set.
  var repoState = '';

  var feedEls = { list: null, title: null, count: null };
  var winEls = { from: null, to: null, reset: null, hint: null };
  var facetClearEl = null;
  var facetChipsEl = null; // container the removable facet chips (R21) render into

  var brush = { active: false, pan: false, moved: false, x0: 0, x1: 0 };

  // ---- formatting ----
  function pad(n) { return n < 10 ? '0' + n : '' + n; }
  function fmtDateTime(ms) {
    var d = new Date(ms);
    return MONTHS[d.getMonth()] + ' ' + d.getDate() + ', ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  function fmtTick(ms) {
    var d = new Date(ms);
    if (state.windowMs <= 3 * MS_PER_DAY) {
      return MONTHS[d.getMonth()] + ' ' + d.getDate() + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    }
    return MONTHS[d.getMonth()] + ' ' + d.getDate();
  }
  function toLocalInput(ms) {
    var d = new Date(ms);
    return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
      'T' + pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  function repoShortName(repo) {
    // Strip a trailing ".git" suffix — together with any slash(es)
    // immediately before or after it — BEFORE trimming trailing slashes,
    // mirroring commitURL's own fix below. Stripping trailing slashes first
    // (the prior, buggy order) can strand a slash that preceded ".git" once
    // ".git" is removed (e.g. ".../repo/.git" -> ".../repo/"), which then
    // becomes the *last* path separator and reduces to an empty segment.
    var r = repo.replace(/\/*\.git\/*$/, '').replace(/\/+$/, '');
    var i = r.lastIndexOf('/');
    var name = i >= 0 ? r.slice(i + 1) : r;
    return name || repo;
  }
  function commitURL(repo, sha) {
    if (!sha || !/^https?:\/\//.test(repo)) { return ''; }
    // Strip a trailing ".git" suffix — together with any slash(es)
    // immediately before or after it ("/.git", ".git/", "/.git/", …) — BEFORE
    // trimming trailing slashes. Stripping trailing slashes first (the prior,
    // buggy order) can leave a slash that preceded ".git" stranded once
    // ".git" is removed (e.g. ".../repo/.git" -> ".../repo/"), which then
    // collides with the leading "/" of "/commit/<sha>" into a double slash.
    var base = repo.replace(/\/*\.git\/*$/, '').replace(/\/+$/, '');
    return base + '/commit/' + sha;
  }
  function repoColor(repo) {
    return REPO_COLORS[repo] || REPO_COLORS[repoShortName(repo)] || DEFAULT_COLOR;
  }

  // changesetKey returns the (repo, commitSha) identity string a Changeset
  // is keyed by for merge/de-dup purposes (R25) — the same pair
  // QueryChangesets' cursor pages by, so a page boundary can never split a
  // Changeset's identity. Defensive against a non-object or missing-field
  // entry: coerced via String() rather than thrown on, so a malformed page
  // entry can never crash the merge. Joined via JSON.stringify of the
  // [repo, commitSha] pair — never a literal-delimiter join (e.g.
  // `repo + ' ' + commitSha`) — because repo/commitSha are untrusted,
  // server-observed strings that can themselves contain any delimiter a
  // fixed join would pick; JSON.stringify's length-prefixed-by-construction
  // encoding of each element can't be reproduced by two different (repo,
  // commitSha) pairs.
  function changesetKey(cs) {
    if (!cs || typeof cs !== 'object') { return String(cs); }
    return JSON.stringify([String(cs.repo), String(cs.commitSha)]);
  }
  // mergeChangesetPage (R25) is the pure transformation behind "Load more":
  // it merges a newly-fetched page's Changesets into the already-rendered
  // list without ever duplicating or dropping one already present, keyed by
  // (repo, commitSha). Existing entries keep their original relative order
  // and come first; each incoming entry not already present is appended in
  // the order it arrived. Returns a NEW array and never mutates either
  // input. Guards a non-array existing/incoming (treated as empty) so a
  // defensive caller never has to pre-validate.
  function mergeChangesetPage(existing, incoming) {
    var existingArr = Array.isArray(existing) ? existing : [];
    var incomingArr = Array.isArray(incoming) ? incoming : [];
    // Object.create(null) — not {} — so a changesetKey() result that happens
    // to equal a magic Object.prototype key name (e.g. "__proto__", reachable
    // via the non-object fallback branch above for a malformed entry) is a
    // plain own data property here, never the object's own [[Prototype]]
    // assignment that an {} literal's inherited __proto__ setter would
    // silently swallow (which would make that key's "have I seen this
    // already" check always false, defeating de-dup for it).
    var seen = Object.create(null);
    existingArr.forEach(function (cs) { seen[changesetKey(cs)] = true; });
    var merged = existingArr.slice();
    incomingArr.forEach(function (cs) {
      var key = changesetKey(cs);
      if (!Object.prototype.hasOwnProperty.call(seen, key)) {
        seen[key] = true;
        merged.push(cs);
      }
    });
    return merged;
  }

  function windowStart() { return state.windowEnd - state.windowMs; }
  function xForTime(t) { return ((t - windowStart()) / state.windowMs) * trackWidth; }
  function timeForX(px) { return windowStart() + (px / trackWidth) * state.windowMs; }
  function csTime(cs) { return new Date(cs.committedAt).getTime(); }
  function inWindow(cs) { var t = csTime(cs); return t >= windowStart() && t <= state.windowEnd; }

  // ---- facet filter ----
  function setFacetState(facet, value, s) {
    if (s === 'off') {
      if (facetState[facet]) {
        delete facetState[facet][value];
        if (Object.keys(facetState[facet]).length === 0) { delete facetState[facet]; }
      }
      return;
    }
    if (!facetState[facet]) { facetState[facet] = {}; }
    facetState[facet][value] = s;
  }
  function getFacetState(facet, value) { return (facetState[facet] && facetState[facet][value]) || 'off'; }
  function cycleFacetState(facet, value) {
    var cur = getFacetState(facet, value);
    var next = FACET_STATE_CYCLE[(FACET_STATE_CYCLE.indexOf(cur) + 1) % FACET_STATE_CYCLE.length];
    setFacetState(facet, value, next);
    return next;
  }
  function activeFilterCount() {
    var n = 0;
    for (var f in facetState) { if (Object.prototype.hasOwnProperty.call(facetState, f)) { n += Object.keys(facetState[f]).length; } }
    return n;
  }
  // facetChips is a pure mapping from facetState to the chip model that
  // drives the filter bar's removable chips (R21/R24): one {facet, value,
  // mode} entry per active (include/exclude) facet/value pair, sorted by
  // facet then value for a deterministic render order, regardless of
  // insertion order. It never mutates facetState (or anything reachable from
  // it) and always returns a fresh array — callers (renderFacetChips) may
  // read the result but must never treat it as shared/cached state. Guards
  // against malformed nesting (a non-object facetState, or a non-object
  // per-facet value map) so a defensive caller never has to pre-validate.
  function facetChips(facetState) {
    if (!facetState || typeof facetState !== 'object') { return []; }
    var chips = [];
    Object.keys(facetState).sort().forEach(function (facet) {
      var values = facetState[facet];
      if (!values || typeof values !== 'object') { return; }
      Object.keys(values).sort().forEach(function (value) {
        var mode = values[value];
        if (mode === 'include' || mode === 'exclude') {
          chips.push({ facet: facet, value: value, mode: mode });
        }
      });
    });
    return chips;
  }

  function buildFilterParams() {
    var pairs = [];
    for (var facet in facetState) {
      if (!Object.prototype.hasOwnProperty.call(facetState, facet)) { continue; }
      var values = facetState[facet];
      for (var value in values) {
        if (!Object.prototype.hasOwnProperty.call(values, value)) { continue; }
        pairs.push([facet, values[value] === 'exclude' ? '-' + value : value]);
      }
    }
    // The repo scope (R26) composes with the facet params via AND on the
    // server (R27): both reach /api/changesets as query params, and the
    // server's FilterSpec ANDs them. An unselected repo ("") emits no pair
    // at all — the no-op invariant.
    if (repoState) { pairs.push(['repo', repoState]); }
    return pairs;
  }
  function buildQueryString(pairs) {
    return pairs.map(function (p) { return encodeURIComponent(p[0]) + '=' + encodeURIComponent(p[1]); }).join('&');
  }

  // fetchChangesetsPage (R25) fetches one page of Changesets from
  // /api/changesets: the first page when cursor is '' (the cursor param is
  // omitted entirely, matching the server's own "empty cursor means start
  // from the top" contract), or the next page when cursor is a prior
  // response's nextCursor. onDone is always called with a
  // {changesets, nextCursor, error} object — never a bare array — so callers
  // never have to special-case the first fetch vs. a subsequent one. Any
  // failure (non-200, malformed JSON, network error) reports error: true
  // alongside an empty page — deliberately kept DISTINCT from a genuinely
  // successful response with an empty nextCursor (the server's real
  // end-of-data signal), so a caller like loadMore can tell "a transient
  // hiccup happened, the Load more control should stay" apart from "there is
  // truly nothing more to load" instead of the two collapsing into the same
  // shape. A successful response never sets error (only the three failure
  // paths below do), so callers can test `if (page.error)` without also
  // checking nextCursor.
  function fetchChangesetsPage(cursor, onDone) {
    var pairs = buildFilterParams();
    pairs.push(['limit', String(BACKDROP_LIMIT)]);
    if (cursor) { pairs.push(['cursor', cursor]); }
    var xhr = new XMLHttpRequest();
    xhr.open('GET', API_PATH + '?' + buildQueryString(pairs), true);
    xhr.onload = function () {
      if (xhr.status !== 200) { onDone({ changesets: [], nextCursor: '', error: true }); return; }
      try {
        var parsed = JSON.parse(xhr.responseText);
        onDone({ changesets: parsed.changesets || [], nextCursor: parsed.nextCursor || '' });
      } catch (e) { onDone({ changesets: [], nextCursor: '', error: true }); }
    };
    xhr.onerror = function () { onDone({ changesets: [], nextCursor: '', error: true }); };
    xhr.send();
  }

  function svgEl(name, attrs) {
    var el = document.createElementNS('http://www.w3.org/2000/svg', name);
    for (var k in attrs) { if (Object.prototype.hasOwnProperty.call(attrs, k)) { el.setAttribute(k, attrs[k]); } }
    return el;
  }

  function clusterFlags(changesets) {
    var withX = changesets
      .map(function (cs) { return { cs: cs, x: xForTime(csTime(cs)) }; })
      .filter(function (item) { return item.x >= 0 && item.x <= trackWidth; })
      .sort(function (a, b) { return a.x - b.x; });
    var clusters = [];
    for (var i = 0; i < withX.length; i++) {
      var item = withX[i];
      var last = clusters[clusters.length - 1];
      if (last && item.x - last.x <= CLUSTER_PIXEL_RADIUS) {
        last.members.push(item.cs);
        last.x = (last.x * (last.members.length - 1) + item.x) / last.members.length;
      } else {
        clusters.push({ x: item.x, members: [item.cs] });
      }
    }
    return clusters;
  }

  // ---- detail + chart diff ----
  var detailPanel = null;
  // detailHost is where a clicked row's detail panel mounts: #timeline-root
  // on the Timeline page, or the feed's own #feed-panel on a track-less page
  // (e.g. the Changes page) that renders the same feed via this same script
  // but omits the timeline track — resolved once in init().
  var detailHost = null;
  var clickGeneration = 0;

  function ensureDetailPanel() {
    if (!detailPanel && detailHost) {
      detailPanel = document.createElement('div');
      detailPanel.id = 'timeline-detail-panel';
      detailHost.appendChild(detailPanel);
    }
    return detailPanel;
  }
  function fetchFragment(url, onDone) {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onload = function () { onDone(xhr.status === 200 ? xhr.responseText : ''); };
    xhr.onerror = function () { onDone(''); };
    xhr.send();
  }
  function fetchChangesetDetail(repo, sha, onDone) {
    fetchFragment(DETAIL_API_PATH + '?repo=' + encodeURIComponent(repo) + '&commitSha=' + encodeURIComponent(sha), onDone);
  }
  function fetchChartDiff(repo, sha, path, onDone) {
    fetchFragment(CHART_DIFF_API_PATH + '?repo=' + encodeURIComponent(repo) + '&commitSha=' + encodeURIComponent(sha) + '&path=' + encodeURIComponent(path), onDone);
  }
  function fetchPlanDiff(repo, sha, path, onDone) {
    fetchFragment(PLAN_DIFF_API_PATH + '?repo=' + encodeURIComponent(repo) + '&commitSha=' + encodeURIComponent(sha) + '&path=' + encodeURIComponent(path), onDone);
  }
  function classifyDiffLine(line) {
    var c = line.charAt(0);
    if (c === '+') { return 'add'; }
    if (c === '-') { return 'del'; }
    return 'ctx';
  }
  function buildHunks(lines) {
    var rows = lines.map(function (l) { return { t: classifyDiffLine(l), text: l }; });
    var out = [], i = 0;
    while (i < rows.length) {
      if (rows[i].t !== 'ctx') { out.push(rows[i]); i++; continue; }
      var j = i;
      while (j < rows.length && rows[j].t === 'ctx') { j++; }
      var runLen = j - i;
      var keepLead = out.length === 0 ? 0 : DIFF_CONTEXT;
      var keepTrail = j >= rows.length ? 0 : DIFF_CONTEXT;
      if (runLen <= keepLead + keepTrail + 1) {
        for (var k = i; k < j; k++) { out.push(rows[k]); }
      } else {
        for (var a = i; a < i + keepLead; a++) { out.push(rows[a]); }
        var hidden = runLen - keepLead - keepTrail;
        out.push({ t: 'gap', text: '⋯ ' + hidden + ' unchanged line' + (hidden === 1 ? '' : 's') });
        for (var b = j - keepTrail; b < j; b++) { out.push(rows[b]); }
      }
      i = j;
    }
    return out;
  }
  function transformChartDiff(slot) {
    var pre = slot.querySelector('.chart-diff-unified');
    if (!pre) { return; }
    var lines = pre.textContent.split('\n');
    if (lines.length && lines[lines.length - 1] === '') { lines.pop(); }
    if (lines.length === 0) { return; }
    var container = document.createElement('div');
    container.className = 'diff-hunks';
    buildHunks(lines).forEach(function (item) {
      var rowEl = document.createElement('div');
      rowEl.className = 'diff-line diff-' + item.t;
      rowEl.textContent = item.text;
      container.appendChild(rowEl);
    });
    pre.parentNode.replaceChild(container, pre);
  }
  function loadChartDiffsForChangeset(subtree, cs, gen) {
    if (!subtree || !subtree.querySelectorAll) { return; }
    var slots = subtree.querySelectorAll('.change-helm-diff-slot');
    for (var i = 0; i < slots.length; i++) {
      (function (slot) {
        slot.textContent = 'Rendering diff…';
        fetchChartDiff(cs.repo, cs.commitSha, slot.getAttribute('data-tenant-path') || '', function (html) {
          if (gen !== clickGeneration) { return; }
          if (html) { slot.innerHTML = html; transformChartDiff(slot); }
          else { slot.textContent = 'Could not load diff.'; }
        });
      })(slots[i]);
    }
  }
  function transformPlanDiff(slot) {
    var pre = slot.querySelector('.plan-diff-unified');
    if (!pre) { return; }
    var lines = pre.textContent.split('\n');
    if (lines.length && lines[lines.length - 1] === '') { lines.pop(); }
    if (lines.length === 0) { return; }
    var container = document.createElement('div');
    container.className = 'diff-hunks';
    buildHunks(lines).forEach(function (item) {
      var rowEl = document.createElement('div');
      rowEl.className = 'diff-line diff-' + item.t;
      rowEl.textContent = item.text;
      container.appendChild(rowEl);
    });
    pre.parentNode.replaceChild(container, pre);
  }
  function loadPlanDiffsForChangeset(subtree, cs, gen) {
    if (!subtree || !subtree.querySelectorAll) { return; }
    var slots = subtree.querySelectorAll('.change-plan-diff-slot');
    for (var i = 0; i < slots.length; i++) {
      (function (slot) {
        slot.textContent = 'Loading resource-change view…';
        fetchPlanDiff(cs.repo, cs.commitSha, slot.getAttribute('data-tenant-path') || '', function (html) {
          if (gen !== clickGeneration) { return; }
          if (html) { slot.innerHTML = html; transformPlanDiff(slot); }
          else { slot.textContent = 'Could not load resource-change view.'; }
        });
      })(slots[i]);
    }
  }
  function onFlagClick(changesets) {
    var panel = ensureDetailPanel();
    if (!panel) { return; }
    clickGeneration++;
    var gen = clickGeneration;
    panel.innerHTML = '';
    changesets.forEach(function (cs) {
      fetchChangesetDetail(cs.repo, cs.commitSha, function (html) {
        if (gen !== clickGeneration || !html) { return; }
        panel.insertAdjacentHTML('beforeend', html);
        loadChartDiffsForChangeset(panel.lastElementChild, cs, gen);
        loadPlanDiffsForChangeset(panel.lastElementChild, cs, gen);
      });
    });
    panel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  }

  // ---- render ----
  function render() {
    if (!svg) { return; }
    while (svg.firstChild) { svg.removeChild(svg.firstChild); }

    // In-progress selection band (only while dragging to zoom).
    if (brush.active && !brush.pan) {
      var bs = Math.min(brush.x0, brush.x1), be = Math.max(brush.x0, brush.x1);
      svg.appendChild(svgEl('rect', { x: bs, y: 0, width: Math.max(0, be - bs), height: trackHeight, fill: '#0d6efd', opacity: 0.10 }));
      [bs, be].forEach(function (bx) {
        svg.appendChild(svgEl('line', { x1: bx, y1: 0, x2: bx, y2: trackHeight, stroke: '#0d6efd', 'stroke-width': 1.5 }));
      });
    }

    // Dated axis.
    svg.appendChild(svgEl('line', { x1: 0, y1: trackMidY, x2: trackWidth, y2: trackMidY, stroke: '#dee2e6', 'stroke-width': 2 }));
    for (var i = 0; i <= AXIS_TICKS; i++) {
      var tx = (i / AXIS_TICKS) * trackWidth;
      svg.appendChild(svgEl('line', { x1: tx, y1: trackMidY - 5, x2: tx, y2: trackMidY + 5, stroke: '#ced4da', 'stroke-width': 1 }));
      var label = svgEl('text', {
        x: Math.min(Math.max(tx, 2), trackWidth - 2), y: trackMidY + 22,
        'text-anchor': i === 0 ? 'start' : (i === AXIS_TICKS ? 'end' : 'middle'),
        'font-size': 10, fill: '#868e96'
      });
      label.textContent = fmtTick(timeForX(tx));
      svg.appendChild(label);
    }

    // Flags (re-clustered every render — zooming in splits stacks apart).
    clusterFlags(state.changesets).forEach(function (cluster) {
      if (cluster.members.length === 1) {
        var cs = cluster.members[0];
        var circle = svgEl('circle', {
          cx: cluster.x, cy: trackMidY, r: 6, fill: repoColor(cs.repo), cursor: 'pointer',
          'data-commit-sha': cs.commitSha
        });
        var title = svgEl('title', {});
        title.textContent = repoShortName(cs.repo) + ' · ' + fmtDateTime(csTime(cs));
        circle.appendChild(title);
        circle.addEventListener('click', function () { onFlagClick([cs]); });
        svg.appendChild(circle);
      } else {
        var group = svgEl('g', { cursor: 'zoom-in' });
        group.appendChild(svgEl('circle', { cx: cluster.x, cy: trackMidY, r: 10, fill: '#495057' }));
        var count = svgEl('text', { x: cluster.x, y: trackMidY + 4, 'text-anchor': 'middle', 'font-size': 10, fill: '#fff' });
        count.textContent = String(cluster.members.length);
        group.appendChild(count);
        var gt = svgEl('title', {});
        gt.textContent = cluster.members.length + ' changes — click to expand';
        group.appendChild(gt);
        // Clicking a cluster zooms into its own span so its members split apart.
        group.addEventListener('click', function () { zoomToMembers(cluster.members); });
        svg.appendChild(group);
      }
    });
  }

  function dataSpan() {
    if (state.changesets.length === 0) { return null; }
    var min = Infinity, max = -Infinity;
    state.changesets.forEach(function (cs) { var t = csTime(cs); if (t < min) { min = t; } if (t > max) { max = t; } });
    return { min: min, max: max };
  }

  function fitWindowToData() {
    var span = dataSpan();
    if (!span) { state.windowEnd = Date.now(); state.windowMs = DEFAULT_WINDOW_MS; return; }
    var pad = Math.max((span.max - span.min) * 0.08, 30 * 60 * 1000);
    state.windowEnd = span.max + pad;
    state.windowMs = clampWindow((span.max - span.min) + 2 * pad);
  }

  function clampWindow(ms) { return Math.max(MIN_WINDOW_MS, Math.min(MAX_WINDOW_MS, ms)); }

  // windowCoversAllData reports whether the visible window already spans
  // every currently-loaded Changeset (i.e. the user has not manually zoomed
  // to a sub-range). Drives the embedded Reset zoom control's disabled state
  // (syncWindowInputs) and, since R25, loadMore's decision on whether
  // re-fitting the window after a Load more is safe (won't yank a
  // manually-chosen view out from under the user).
  function windowCoversAllData() {
    var span = dataSpan();
    return !span || (windowStart() <= span.min && state.windowEnd >= span.max);
  }

  function setWindow(startMs, endMs) {
    if (endMs <= startMs) { return; }
    state.windowMs = clampWindow(endMs - startMs);
    state.windowEnd = startMs + state.windowMs;
    afterWindowChange();
  }

  function zoomToMembers(members) {
    var min = Infinity, max = -Infinity;
    members.forEach(function (cs) { var t = csTime(cs); if (t < min) { min = t; } if (t > max) { max = t; } });
    var pad = Math.max((max - min) * 0.5, 60 * 1000);
    setWindow(min - pad, max + pad);
  }

  function resetView() {
    state.hasFitWindow = true;
    fitWindowToData();
    afterWindowChange();
  }

  function afterWindowChange() {
    render();
    syncWindowInputs();
    renderFeed();
  }

  function syncWindowInputs() {
    if (winEls.from) { winEls.from.value = toLocalInput(windowStart()); }
    if (winEls.to) { winEls.to.value = toLocalInput(state.windowEnd); }
    if (winEls.reset) {
      // "Reset" is meaningful whenever the view doesn't already cover all data.
      winEls.reset.disabled = windowCoversAllData();
    }
  }

  // ---- feed ----
  // buildEmptyRow renders the loading / nothing-recorded-yet / nothing-in-
  // window-or-filters states as a single full-width row (one <td> spanning
  // every column) appended directly into the feed's own <tbody> — table form
  // (R16), not a bare table with headers over nothing. withClear adds the
  // "Clear filters & reset zoom" affordance (R16c); loading never does.
  function buildEmptyRow(msg, withClear) {
    var tr = document.createElement('tr');
    tr.className = 'feed-empty-row';
    var td = document.createElement('td');
    td.colSpan = FEED_COLUMN_COUNT;
    var span = document.createElement('span');
    span.textContent = msg;
    td.appendChild(span);
    if (withClear) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'feed-clear-btn';
      btn.textContent = 'Clear filters & reset zoom';
      btn.addEventListener('click', clearAllFilters);
      td.appendChild(btn);
    }
    tr.appendChild(td);
    return tr;
  }
  function buildFeedRow(cs) {
    var tr = document.createElement('tr');
    tr.className = 'feed-row';
    tr.addEventListener('click', function () { onFlagClick([cs]); });

    var whenCell = document.createElement('td');
    whenCell.className = 'feed-cell-when';
    whenCell.textContent = fmtDateTime(csTime(cs));
    tr.appendChild(whenCell);

    var repoCell = document.createElement('td');
    repoCell.className = 'feed-cell-repo';
    var dot = document.createElement('span');
    dot.className = 'feed-dot';
    dot.style.background = repoColor(cs.repo);
    repoCell.appendChild(dot);
    var repoName = document.createElement('span');
    repoName.className = 'feed-repo';
    repoName.textContent = repoShortName(cs.repo);
    repoName.title = cs.repo;
    repoCell.appendChild(repoName);
    tr.appendChild(repoCell);

    var commitCell = document.createElement('td');
    commitCell.className = 'feed-cell-commit';
    var url = commitURL(cs.repo, cs.commitSha);
    var sha = cs.commitSha.slice(0, 8);
    if (url) {
      var a = document.createElement('a');
      a.className = 'feed-commit';
      a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = sha; a.title = cs.commitSha;
      a.addEventListener('click', function (e) { e.stopPropagation(); });
      commitCell.appendChild(a);
    } else {
      var shaEl = document.createElement('span');
      shaEl.className = 'feed-commit feed-commit-plain';
      shaEl.textContent = sha; shaEl.title = cs.commitSha;
      commitCell.appendChild(shaEl);
    }
    tr.appendChild(commitCell);

    var authorCell = document.createElement('td');
    authorCell.className = 'feed-cell-author';
    authorCell.textContent = cs.author;
    tr.appendChild(authorCell);

    var n = (cs.changes || []).length;
    var changesCell = document.createElement('td');
    changesCell.className = 'feed-cell-changes';
    changesCell.textContent = n + (n === 1 ? ' change' : ' changes');
    tr.appendChild(changesCell);

    return tr;
  }
  // buildLoadMoreRow (R25) renders the "Load more" affordance as one more
  // full-width row (one <td> spanning every column) appended after the
  // visible feed rows — the same in-table-row convention buildEmptyRow uses
  // for loading/empty states, so the feed never needs a footer outside the
  // <table> for this. Disabled + relabeled while a fetch is already in
  // flight (state.loadingMore) so a fast double-click can't be mistaken for
  // two separate requests. When the most recent attempt failed
  // (state.loadMoreError), the button stays in place — clicking it retries —
  // alongside a brief inline message, so a transient fetch failure is
  // visible instead of silently looking identical to "no more data".
  function buildLoadMoreRow() {
    var tr = document.createElement('tr');
    tr.className = 'feed-load-more-row';
    var td = document.createElement('td');
    td.colSpan = FEED_COLUMN_COUNT;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'feed-load-more-btn';
    btn.textContent = state.loadingMore ? 'Loading more…' : 'Load more';
    btn.disabled = state.loadingMore;
    btn.addEventListener('click', loadMore);
    td.appendChild(btn);
    if (state.loadMoreError && !state.loadingMore) {
      var err = document.createElement('span');
      err.className = 'feed-load-more-error';
      err.textContent = ' Could not load more — try again.';
      td.appendChild(err);
    }
    tr.appendChild(td);
    return tr;
  }
  // buildBackdropErrorRow renders the honest-failure counterpart to
  // buildEmptyRow's "No changes recorded yet" message: a full-width row (the
  // same in-table-row convention buildEmptyRow/buildLoadMoreRow both use)
  // shown whenever state.backdropError is true (a loadBackdrop() fetch
  // failed — non-200/malformed JSON/network error). Reuses the
  // feed-load-more-error inline-message class for the danger-colored text —
  // the same visual treatment loadMore's own failure already uses — and the
  // feed-clear-btn class for the Retry action (already styled as a small
  // outlined button; the action here just re-fires loadBackdrop instead of
  // clearing filters). Also carries feed-empty-row so it inherits that
  // class's centered layout/padding without a second, duplicate CSS rule.
  function buildBackdropErrorRow() {
    var tr = document.createElement('tr');
    tr.className = 'feed-empty-row feed-backdrop-error-row';
    var td = document.createElement('td');
    td.colSpan = FEED_COLUMN_COUNT;
    var span = document.createElement('span');
    span.className = 'feed-load-more-error';
    span.textContent = 'Could not load changes — try again.';
    td.appendChild(span);
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'feed-clear-btn';
    btn.textContent = 'Retry';
    btn.addEventListener('click', loadBackdrop);
    td.appendChild(btn);
    tr.appendChild(td);
    return tr;
  }
  // maybeAppendLoadMoreRow appends buildLoadMoreRow() whenever
  // state.nextCursor is non-empty, regardless of how many rows (if any) are
  // currently visible. Extracted so the guard lives at a single chokepoint —
  // renderFeed calls it both after the zero-visible-window empty state and
  // after the normal visible-rows loop, so a user zoomed into a window with
  // no currently-visible Changesets can still trigger loadMore() to page in
  // the next (older) batch, rather than being forced to reset zoom first.
  function maybeAppendLoadMoreRow() {
    if (state.nextCursor) { feedEls.list.appendChild(buildLoadMoreRow()); }
  }
  function renderFeed() {
    if (!feedEls.list) { return; }
    while (feedEls.list.firstChild) { feedEls.list.removeChild(feedEls.list.firstChild); }

    if (!state.loaded) {
      if (feedEls.title) { feedEls.title.textContent = 'Changes'; }
      if (feedEls.count) { feedEls.count.textContent = ''; }
      feedEls.list.appendChild(buildEmptyRow('Loading changes…', false));
      return;
    }

    var visible = state.changesets.filter(inWindow).sort(function (a, b) { return csTime(b) - csTime(a); });
    var total = state.changesets.length;
    var zoomed = visible.length !== total;

    if (feedEls.title) {
      feedEls.title.textContent = zoomed
        ? 'Changes — ' + fmtDateTime(windowStart()) + ' → ' + fmtDateTime(state.windowEnd)
        : 'Changes';
    }
    if (feedEls.count) { feedEls.count.textContent = total === 0 ? '' : visible.length + ' of ' + total; }

    // A backdrop fetch failure (state.backdropError) always gets its own
    // honest error row, appended before anything else — whether or not there
    // is prior data to show alongside it. When total === 0 that row IS the
    // entire feed body (never the misleading "No changes recorded yet"
    // empty state, which describes a genuinely empty SUCCESSFUL response).
    // When total > 0 (a filter-reload failure that left previously-loaded
    // Changesets in place, per renderBackdrop's own care above), the error
    // row is a banner ahead of those still-showing rows, so stale-but-real
    // data stays visible alongside the honest failure notice.
    if (state.backdropError) { feedEls.list.appendChild(buildBackdropErrorRow()); }

    if (total === 0) {
      if (!state.backdropError) {
        feedEls.list.appendChild(buildEmptyRow('No changes recorded yet — the poller may still be backfilling.', activeFilterCount() > 0));
      }
      return;
    }
    if (visible.length === 0) {
      feedEls.list.appendChild(buildEmptyRow('No changes in this window' + (activeFilterCount() > 0 ? ' or matching the current filters.' : '.'), true));
      maybeAppendLoadMoreRow();
      return;
    }
    visible.forEach(function (cs) { feedEls.list.appendChild(buildFeedRow(cs)); });
    maybeAppendLoadMoreRow();
  }

  // ---- facet dropdowns ----
  function collectServerFacets(container) {
    facetValues = {};
    var btns = container.querySelectorAll('[data-facet][data-value]');
    for (var i = 0; i < btns.length; i++) {
      var f = btns[i].getAttribute('data-facet'), v = btns[i].getAttribute('data-value');
      if (!facetValues[f]) { facetValues[f] = []; }
      if (facetValues[f].indexOf(v) < 0) { facetValues[f].push(v); }
    }
  }
  function refreshFacetBadge(facet) {
    var badge = badgeEls[facet];
    if (!badge) { return; }
    var n = facetState[facet] ? Object.keys(facetState[facet]).length : 0;
    badge.textContent = n ? String(n) : '';
    badge.style.display = n ? '' : 'none';
  }
  function refreshAllFacetPills() {
    for (var f in pillEls) {
      if (!Object.prototype.hasOwnProperty.call(pillEls, f)) { continue; }
      for (var v in pillEls[f]) {
        if (Object.prototype.hasOwnProperty.call(pillEls[f], v)) { pillEls[f][v].setAttribute('data-state', getFacetState(f, v)); }
      }
      refreshFacetBadge(f);
    }
  }
  function refreshFacetClear() {
    if (facetClearEl) { facetClearEl.hidden = activeFilterCount() === 0; }
  }
  // removeFacetChip (R21) removes a single chip: it drives the facet/value
  // pair back to 'off' through the same setFacetState machinery the pill
  // cycle and "only" shortcut use (never a bespoke path), then re-syncs every
  // piece of facet chrome (pills, badges, clear control, chips) and refetches.
  function removeFacetChip(facet, value) {
    setFacetState(facet, value, 'off');
    refreshAllFacetPills();
    refreshFacetChrome();
    onFilterChanged();
  }
  // buildFacetChip renders one chip element from a facetChips() entry: a
  // facet/value label plus a remove button, wired with addEventListener (no
  // inline handlers) and built entirely from createElement + textContent —
  // facet/value strings come from server-observed data and must never reach
  // innerHTML. The mode-named class (facet-chip-include / facet-chip-exclude)
  // is what makes include vs exclude visually distinct in the filter bar
  // (R23); the CSS rules live in timeline_template.go's inline <style>.
  function buildFacetChip(chip) {
    var el = document.createElement('span');
    el.className = 'facet-chip facet-chip-' + chip.mode;
    el.setAttribute('data-facet', chip.facet);
    el.setAttribute('data-value', chip.value);

    var label = document.createElement('span');
    label.className = 'facet-chip-label';
    label.textContent = chip.facet + ': ' + chip.value;
    el.appendChild(label);

    var remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'facet-chip-remove';
    remove.textContent = '×';
    remove.setAttribute('aria-label', 'Remove filter ' + chip.facet + ': ' + chip.value);
    remove.addEventListener('click', function () { removeFacetChip(chip.facet, chip.value); });
    el.appendChild(remove);

    return el;
  }
  // renderFacetChips (R21/R24) re-renders the chip row from the pure
  // facetChips(facetState) mapping — the chip model is never hand-assembled
  // here, only mapped to DOM.
  function renderFacetChips() {
    if (!facetChipsEl) { return; }
    while (facetChipsEl.firstChild) { facetChipsEl.removeChild(facetChipsEl.firstChild); }
    facetChips(facetState).forEach(function (chip) { facetChipsEl.appendChild(buildFacetChip(chip)); });
  }
  // refreshFacetChrome re-syncs every facetState-derived affordance that
  // isn't a specific pill/badge: the "Clear all filters" control's
  // visibility (R22) and the chip row (R21/R24). Every call site that used to
  // call refreshFacetClear() alone now calls this, so a chip can never drift
  // out of sync with the pills/badges it mirrors.
  function refreshFacetChrome() {
    refreshFacetClear();
    renderFacetChips();
  }
  function buildFacetDropdowns(container) {
    collectServerFacets(container);
    container.innerHTML = '';
    container.classList.add('facet-dropdowns');
    Object.keys(facetValues).sort().forEach(function (facet) {
      var dd = document.createElement('details');
      dd.className = 'facet-dd';
      var summary = document.createElement('summary');
      var name = document.createElement('span');
      name.className = 'facet-dd-name';
      name.textContent = facet;
      summary.appendChild(name);
      var badge = document.createElement('span');
      badge.className = 'facet-dd-badge';
      badge.style.display = 'none';
      summary.appendChild(badge);
      badgeEls[facet] = badge;
      dd.appendChild(summary);

      var body = document.createElement('div');
      body.className = 'facet-dd-body';
      pillEls[facet] = {};
      facetValues[facet].sort().forEach(function (value) {
        var rowEl = document.createElement('div');
        rowEl.className = 'facet-row';
        var pill = document.createElement('button');
        pill.type = 'button';
        pill.className = 'facet-pill';
        pill.setAttribute('data-state', getFacetState(facet, value));
        pill.textContent = value;
        pill.addEventListener('click', function () {
          pill.setAttribute('data-state', cycleFacetState(facet, value));
          refreshFacetBadge(facet); refreshFacetChrome(); onFilterChanged();
        });
        pillEls[facet][value] = pill;
        rowEl.appendChild(pill);

        var only = document.createElement('button');
        only.type = 'button';
        only.className = 'facet-only';
        only.textContent = 'only';
        only.title = 'Include only this value';
        only.addEventListener('click', function () {
          facetValues[facet].forEach(function (other) { setFacetState(facet, other, other === value ? 'include' : 'exclude'); });
          for (var v in pillEls[facet]) {
            if (Object.prototype.hasOwnProperty.call(pillEls[facet], v)) { pillEls[facet][v].setAttribute('data-state', getFacetState(facet, v)); }
          }
          refreshFacetBadge(facet); refreshFacetChrome(); onFilterChanged();
        });
        rowEl.appendChild(only);
        body.appendChild(rowEl);
      });
      dd.appendChild(body);
      container.appendChild(dd);
    });
  }
  // clearFacets is the "clear all filters" control's handler (R22, the
  // #facet-clear button in the filter bar): reset every facet to 'off' in
  // one click, re-syncing pills, badges, the clear control itself, and the
  // chip row (via refreshFacetChrome) before refetching.
  function clearFacets() {
    facetState = {};
    refreshAllFacetPills();
    refreshFacetChrome();
    onFilterChanged();
  }
  function clearAllFilters() {
    facetState = {};
    refreshAllFacetPills();
    refreshFacetChrome();
    // Clearing facets refetches the (now unfiltered) backdrop. The window must
    // be fit to that FRESH data, not the stale filtered set we currently hold —
    // so drop the one-shot fit latch and let renderBackdrop re-fit inside the
    // fetch callback. (Fitting synchronously here would run against the old
    // data and, worse, leave hasFitWindow=true so the post-fetch re-fit never
    // runs.) resetView() stays for the standalone "Reset zoom" button, which
    // does not refetch.
    state.hasFitWindow = false;
    loadBackdrop();
  }

  // ---- backdrop load ----
  // backdropGeneration (R25) guards against a loadMore() fetch that is still
  // in flight when a fresh backdrop reload (loadBackdrop) fires — mirroring
  // clickGeneration's exact guard for the analogous stale-async-response
  // hazard around onFlagClick. Bumped on every loadBackdrop call; loadMore
  // captures the generation it fired under and compares it against the
  // current value before merging its (now possibly stale, differently
  // filtered) page into state.
  var backdropGeneration = 0;

  // renderBackdrop (the fetchChangesetsPage onDone for a FRESH first page,
  // fired by loadBackdrop on both initial load and every filter/repo-scope
  // reload) replaces state.changesets/state.nextCursor wholesale ONLY on a
  // SUCCESSFUL fetch — pagination state from a prior filter/window never
  // survives a reload. Contrast with loadMore's onDone below, which merges
  // rather than replaces.
  //
  // On a FAILED fetch (page.error — non-200/malformed JSON/network error,
  // the same three failure paths fetchChangesetsPage's own doc comment
  // describes), state.changesets/state.nextCursor are left completely
  // untouched instead: a fresh initial load has nothing yet to preserve
  // (they're still their initial empty/'' values), but a filter-triggered
  // reload's failure must never silently wipe Changesets that were already
  // showing — the same care loadMore takes not to overwrite state.nextCursor
  // on its own failure, applied here to a fresh load's full replacement
  // instead of an append. state.backdropError drives a distinct, honest
  // error affordance in the feed (buildBackdropErrorRow, below) instead of
  // the "No changes recorded yet" empty state, which describes a genuinely
  // empty SUCCESSFUL response — never a failure.
  function renderBackdrop(page) {
    state.backdropError = !!page.error;
    if (!page.error) {
      state.changesets = page.changesets;
      state.nextCursor = page.nextCursor;
      if (!state.hasFitWindow) { state.hasFitWindow = true; fitWindowToData(); }
    }
    state.loaded = true;
    render();
    syncWindowInputs();
    renderFeed();
  }
  function loadBackdrop() {
    state.loaded = false;
    state.loadMoreError = false;
    state.backdropError = false;
    renderFeed();
    backdropGeneration++;
    var gen = backdropGeneration;
    fetchChangesetsPage('', function (page) {
      if (gen !== backdropGeneration) { return; }
      renderBackdrop(page);
    });
  }
  function onFilterChanged() { loadBackdrop(); }

  // loadMore (R25) fetches the next page using the cursor the last page
  // returned, and merges it into state.changesets (never a replace — the
  // Changesets already loaded and possibly already viewed must never
  // disappear). Guarded against firing a second overlapping fetch (a fast
  // double-click) and against firing with no further page available.
  //
  // windowCoversAllData() is evaluated INSIDE the callback, once the
  // response actually arrives — never captured before the fetch fires.
  // Nothing disables drag-zoom, wheel-zoom, pan, or the From/To inputs while
  // a "Load more" fetch is in flight, so a decision captured at
  // request-time can go stale before the response arrives and would
  // silently yank a zoom/pan the user made in the meantime; evaluating it at
  // use-time picks up any such change. It is evaluated against the
  // PRE-merge changesets/span (before mergeChangesetPage runs below) rather
  // than the post-merge one: the newly-fetched page is, by construction,
  // older than everything already loaded, so a post-merge span's min would
  // almost always fall outside a window that covered only the pre-merge
  // data — checking post-merge would make the refit below effectively never
  // fire, defeating its whole purpose (making the newly-loaded, older
  // Changesets actually visible). So the question this answers is exactly:
  // "as of right now — including anything the user did while this fetch was
  // in flight — does the window still show everything it showed before this
  // page arrived?" If yes, refit so the new page is visible too; if the user
  // has since zoomed to a sub-range, leave it untouched. Also guarded (via
  // backdropGeneration) against a fresh filter reload superseding this fetch
  // before it resolves — see backdropGeneration's own doc comment above
  // loadBackdrop.
  //
  // A fetch failure (page.error) is handled distinctly from a real
  // end-of-data response: state.nextCursor is left untouched (never
  // overwritten with the same '' a legitimate last-page response reports)
  // and state.loadMoreError drives an inline message on the still-present
  // Load more control, so a transient hiccup can never be mistaken for
  // "there is nothing more" or silently swallowed.
  function loadMore() {
    if (state.loadingMore || !state.nextCursor) { return; }
    state.loadingMore = true;
    state.loadMoreError = false;
    renderFeed();
    var gen = backdropGeneration;
    fetchChangesetsPage(state.nextCursor, function (page) {
      state.loadingMore = false;
      if (gen !== backdropGeneration) { return; }
      if (page.error) {
        state.loadMoreError = true;
        renderFeed();
        return;
      }
      var shouldRefitWindow = windowCoversAllData();
      state.changesets = mergeChangesetPage(state.changesets, page.changesets);
      state.nextCursor = page.nextCursor;
      if (shouldRefitWindow) { fitWindowToData(); }
      afterWindowChange();
    });
  }

  // ---- interactions ----
  function zoom(factor) { state.windowMs = clampWindow(state.windowMs / factor); afterWindowChange(); }
  function pan(deltaMs) { state.windowEnd += deltaMs; afterWindowChange(); }
  function svgX(clientX) { return clientX - svg.getBoundingClientRect().left; }

  function attachInteractions() {
    if (!svg) { return; }
    svg.addEventListener('wheel', function (evt) {
      evt.preventDefault();
      zoom(evt.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP);
    });
    // A press that doesn't move is a click (handled by the marker's own click
    // listener — so we must NOT re-render on mousedown, which would replace the
    // very element being clicked). Only once the pointer actually moves past the
    // threshold does it become a drag (band-select to zoom, or shift-drag pan).
    svg.addEventListener('mousedown', function (evt) {
      brush.active = true;
      brush.pan = evt.shiftKey;
      brush.moved = false;
      brush.x0 = brush.x1 = svgX(evt.clientX);
      if (brush.pan) { svg.style.cursor = 'grabbing'; }
    });
    window.addEventListener('mousemove', function (evt) {
      if (!brush.active) { return; }
      var x = svgX(evt.clientX);
      if (!brush.moved && Math.abs(x - brush.x0) < MIN_DRAG_PX) { return; }
      brush.moved = true;
      if (brush.pan) {
        pan(-(x - brush.x1) * (state.windowMs / trackWidth));
        brush.x1 = x;
      } else {
        brush.x1 = x;
        render();
      }
    });
    window.addEventListener('mouseup', function () {
      if (!brush.active) { return; }
      var wasPan = brush.pan, moved = brush.moved;
      var x0 = brush.x0, x1 = brush.x1;
      brush.active = false; brush.pan = false; brush.moved = false;
      svg.style.cursor = 'crosshair';
      // Only a real (moved) non-pan drag zooms; a plain click falls through to
      // the marker's own handler (open detail / expand cluster).
      if (moved && !wasPan) {
        setWindow(timeForX(Math.min(x0, x1)), timeForX(Math.max(x0, x1)));
      }
    });
  }

  // ---- controls ----
  function makeWindowInput(labelText, capture) {
    var label = document.createElement('label');
    label.className = 'range-label';
    label.textContent = labelText + ' ';
    var input = document.createElement('input');
    input.type = 'datetime-local';
    input.addEventListener('change', function () {
      var from = winEls.from && winEls.from.value ? Date.parse(winEls.from.value) : NaN;
      var to = winEls.to && winEls.to.value ? Date.parse(winEls.to.value) : NaN;
      if (!isNaN(from) && !isNaN(to)) { setWindow(from, to); }
    });
    label.appendChild(input);
    capture(input);
    return label;
  }
  function buildControls() {
    var controls = document.createElement('div');
    controls.className = 'timeline-controls';
    controls.appendChild(makeWindowInput('From', function (el) { winEls.from = el; }));
    controls.appendChild(makeWindowInput('To', function (el) { winEls.to = el; }));

    var reset = document.createElement('button');
    reset.type = 'button';
    reset.className = 'range-clear';
    reset.textContent = 'Reset zoom';
    reset.disabled = true;
    reset.addEventListener('click', resetView);
    winEls.reset = reset;
    controls.appendChild(reset);

    var hint = document.createElement('span');
    hint.className = 'timeline-hint';
    hint.textContent = 'Drag to zoom into a range · scroll to zoom · shift-drag to pan · click a cluster to expand';
    winEls.hint = hint;
    controls.appendChild(hint);
    return controls;
  }

  function init() {
    root = document.getElementById('timeline-root');
    // The feed itself, and a clicked row's detail panel, are shared by every
    // page that renders this script (Timeline and Changes) — wired up below
    // unconditionally. The timeline track (SVG + controls), facet dropdowns,
    // and header Reset-zoom action are Timeline-page-only chrome, built only
    // when #timeline-root is actually present on the page.
    detailHost = root || document.getElementById('feed-panel');

    if (root) {
      trackWidth = Math.max(600, (root.clientWidth || 900));

      root.appendChild(buildControls());
      svg = svgEl('svg', { width: trackWidth, height: trackHeight, viewBox: '0 0 ' + trackWidth + ' ' + trackHeight, 'class': 'timeline-svg' });
      svg.style.cursor = 'crosshair';
      root.appendChild(svg);
      attachInteractions();

      var repoFilterEl = document.getElementById('repo-filter');
      if (repoFilterEl) {
        repoFilterEl.addEventListener('change', function () {
          repoState = repoFilterEl.value;
          onFilterChanged();
        });
      }

      var facetContainer = document.getElementById('facet-controls');
      if (facetContainer) { buildFacetDropdowns(facetContainer); }
      facetClearEl = document.getElementById('facet-clear');
      if (facetClearEl) { facetClearEl.addEventListener('click', clearFacets); }
      facetChipsEl = document.getElementById('facet-chips');
      refreshFacetChrome();

      // The observability shell's header carries its own "Reset zoom" action
      // (the global timeline action) alongside the embedded track's own
      // range-clear control built in buildControls() above; both call the same
      // resetView so either affordance returns the visible window to the full
      // data span.
      var headerReset = document.getElementById('header-reset-zoom');
      if (headerReset) { headerReset.addEventListener('click', resetView); }
    }

    feedEls.list = document.getElementById('feed-list');
    feedEls.title = document.getElementById('feed-title');
    feedEls.count = document.getElementById('feed-count');

    loadBackdrop();
  }

  // Guarded on `typeof document` so this file stays require()-able from
  // Node with no DOM present (see commit-link.property.test.js) — a no-op
  // branch in every real browser, where `document` always exists.
  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', init);
    } else {
      init();
    }
  }

  // Node-only export hook for the commit-link property test
  // (commit-link.property.test.js). `module` is never defined in a browser,
  // so this branch never runs client-side — the app still ships as a single
  // first-party <script src="/static/timeline.js"> (R18); no test-only code
  // path is reachable in production. loadMore/loadBackdrop/state are
  // exported for load-more.behavior.test.js's and
  // backdrop-fetch-error.behavior.test.js's async-timing regression
  // coverage; every DOM touch reachable from either
  // (render/syncWindowInputs/renderFeed) is itself guarded on the relevant
  // element existing, so calling them here with no DOM present is safe.
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { commitURL: commitURL, repoShortName: repoShortName, facetChips: facetChips, mergeChangesetPage: mergeChangesetPage, loadMore: loadMore, loadBackdrop: loadBackdrop, state: state };
  }
})();
