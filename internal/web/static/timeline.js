// timeline.js — vendored, embedded (go:embed) client-side rendering for the
// Change Tracking Dashboard timeline. This is the dashboard's only client-side
// script; it stays thin. All querying, grouping, classification, facet
// filtering, and per-kind (chart vs value) detail rendering stay server-side
// (store/changeset/filter and the /api/changesets* endpoints). This file:
//   - fetches Changesets from /api/changesets (backdrop, facet-filtered)
//   - renders one flag per Changeset on a single dated time track
//   - groups the server-rendered facet controls into per-facet dropdowns, each
//     value cycling off -> include -> exclude, plus an "only" shortcut, plus a
//     single "Clear filters" reset
//   - uses the visible window AS the feed filter (Datadog-style): drag on the
//     track to zoom into a range (the feed follows), scroll to zoom, shift-drag
//     to pan, "Reset zoom" to return to the full span; the From/To inputs
//     mirror and drive the window
//   - re-clusters flags on every render so zooming in splits a stacked marker
//     apart; clicking a cluster zooms into its own span to expand it
//   - renders the "Changes" feed for the visible window, with explicit loading
//     / empty states, short repo names, GitHub commit links and day/time stamps
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
    hasFitWindow: false
  };

  var facetState = {};   // facetState[facet][value] = 'include' | 'exclude'
  var facetValues = {};  // facet -> [values]
  var pillEls = {};      // facet -> value -> pill element
  var badgeEls = {};     // facet -> badge element

  var feedEls = { list: null, empty: null, title: null, count: null };
  var winEls = { from: null, to: null, reset: null, hint: null };
  var facetClearEl = null;

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
    var r = repo.replace(/\/+$/, '').replace(/\.git$/, '');
    var i = r.lastIndexOf('/');
    var name = i >= 0 ? r.slice(i + 1) : r;
    return name || repo;
  }
  function commitURL(repo, sha) {
    if (!sha || !/^https?:\/\//.test(repo)) { return ''; }
    return repo.replace(/\/+$/, '').replace(/\.git$/, '') + '/commit/' + sha;
  }
  function repoColor(repo) {
    return REPO_COLORS[repo] || REPO_COLORS[repoShortName(repo)] || DEFAULT_COLOR;
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
    return pairs;
  }
  function buildQueryString(pairs) {
    return pairs.map(function (p) { return encodeURIComponent(p[0]) + '=' + encodeURIComponent(p[1]); }).join('&');
  }

  function fetchBackdrop(onDone) {
    var pairs = buildFilterParams();
    pairs.push(['limit', String(BACKDROP_LIMIT)]);
    var xhr = new XMLHttpRequest();
    xhr.open('GET', API_PATH + '?' + buildQueryString(pairs), true);
    xhr.onload = function () {
      if (xhr.status !== 200) { onDone([]); return; }
      try { onDone((JSON.parse(xhr.responseText).changesets) || []); } catch (e) { onDone([]); }
    };
    xhr.onerror = function () { onDone([]); };
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
  var clickGeneration = 0;

  function ensureDetailPanel() {
    if (!detailPanel && root) {
      detailPanel = document.createElement('div');
      detailPanel.id = 'timeline-detail-panel';
      root.appendChild(detailPanel);
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
      var span = dataSpan();
      // "Reset" is meaningful whenever the view doesn't already cover all data.
      var coversAll = !span || (windowStart() <= span.min && state.windowEnd >= span.max);
      winEls.reset.disabled = coversAll;
    }
  }

  // ---- feed ----
  function showEmpty(msg, withClear) {
    if (feedEls.list) { feedEls.list.style.display = 'none'; }
    if (!feedEls.empty) { return; }
    feedEls.empty.hidden = false;
    feedEls.empty.textContent = '';
    var span = document.createElement('span');
    span.textContent = msg;
    feedEls.empty.appendChild(span);
    if (withClear) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'feed-clear-btn';
      btn.textContent = 'Clear filters & reset zoom';
      btn.addEventListener('click', clearAllFilters);
      feedEls.empty.appendChild(btn);
    }
  }
  function hideEmpty() {
    if (feedEls.empty) { feedEls.empty.hidden = true; }
    if (feedEls.list) { feedEls.list.style.display = ''; }
  }
  function buildFeedRow(cs) {
    var li = document.createElement('li');
    li.className = 'feed-row';
    li.addEventListener('click', function () { onFlagClick([cs]); });

    var dot = document.createElement('span');
    dot.className = 'feed-dot';
    dot.style.background = repoColor(cs.repo);
    li.appendChild(dot);

    var time = document.createElement('span');
    time.className = 'feed-time';
    time.textContent = fmtDateTime(csTime(cs));
    li.appendChild(time);

    var repo = document.createElement('span');
    repo.className = 'feed-repo';
    repo.textContent = repoShortName(cs.repo);
    repo.title = cs.repo;
    li.appendChild(repo);

    var url = commitURL(cs.repo, cs.commitSha);
    var sha = cs.commitSha.slice(0, 8);
    if (url) {
      var a = document.createElement('a');
      a.className = 'feed-commit';
      a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer';
      a.textContent = sha; a.title = cs.commitSha;
      a.addEventListener('click', function (e) { e.stopPropagation(); });
      li.appendChild(a);
    } else {
      var shaEl = document.createElement('span');
      shaEl.className = 'feed-commit feed-commit-plain';
      shaEl.textContent = sha; shaEl.title = cs.commitSha;
      li.appendChild(shaEl);
    }

    var author = document.createElement('span');
    author.className = 'feed-author';
    author.textContent = cs.author;
    li.appendChild(author);

    var n = (cs.changes || []).length;
    var badge = document.createElement('span');
    badge.className = 'feed-count-badge';
    badge.textContent = n + (n === 1 ? ' change' : ' changes');
    li.appendChild(badge);
    return li;
  }
  function renderFeed() {
    if (!feedEls.list) { return; }
    while (feedEls.list.firstChild) { feedEls.list.removeChild(feedEls.list.firstChild); }

    if (!state.loaded) {
      if (feedEls.title) { feedEls.title.textContent = 'Changes'; }
      if (feedEls.count) { feedEls.count.textContent = ''; }
      showEmpty('Loading changes…', false);
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

    if (total === 0) {
      showEmpty('No changes recorded yet — the poller may still be backfilling.', activeFilterCount() > 0);
      return;
    }
    if (visible.length === 0) {
      showEmpty('No changes in this window' + (activeFilterCount() > 0 ? ' or matching the current filters.' : '.'), true);
      return;
    }
    hideEmpty();
    visible.forEach(function (cs) { feedEls.list.appendChild(buildFeedRow(cs)); });
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
          refreshFacetBadge(facet); refreshFacetClear(); onFilterChanged();
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
          refreshFacetBadge(facet); refreshFacetClear(); onFilterChanged();
        });
        rowEl.appendChild(only);
        body.appendChild(rowEl);
      });
      dd.appendChild(body);
      container.appendChild(dd);
    });
  }
  function clearFacets() {
    facetState = {};
    refreshAllFacetPills();
    refreshFacetClear();
    onFilterChanged();
  }
  function clearAllFilters() {
    facetState = {};
    refreshAllFacetPills();
    refreshFacetClear();
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
  function renderBackdrop(changesets) {
    state.changesets = changesets;
    state.loaded = true;
    if (!state.hasFitWindow) { state.hasFitWindow = true; fitWindowToData(); }
    render();
    syncWindowInputs();
    renderFeed();
  }
  function loadBackdrop() {
    state.loaded = false;
    renderFeed();
    fetchBackdrop(renderBackdrop);
  }
  function onFilterChanged() { loadBackdrop(); }

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
    if (!root) { return; }
    trackWidth = Math.max(600, (root.clientWidth || 900));

    root.appendChild(buildControls());
    svg = svgEl('svg', { width: trackWidth, height: trackHeight, viewBox: '0 0 ' + trackWidth + ' ' + trackHeight, 'class': 'timeline-svg' });
    svg.style.cursor = 'crosshair';
    root.appendChild(svg);
    attachInteractions();

    var facetContainer = document.getElementById('facet-controls');
    if (facetContainer) { buildFacetDropdowns(facetContainer); }
    facetClearEl = document.getElementById('facet-clear');
    if (facetClearEl) { facetClearEl.addEventListener('click', clearFacets); }
    refreshFacetClear();

    feedEls.list = document.getElementById('feed-list');
    feedEls.empty = document.getElementById('feed-empty');
    feedEls.title = document.getElementById('feed-title');
    feedEls.count = document.getElementById('feed-count');

    loadBackdrop();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
