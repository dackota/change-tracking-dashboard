// timeline.js — vendored, embedded (go:embed) client-side rendering for the
// Change Tracking Dashboard timeline. This is the dashboard's only client-side
// script; it stays thin. All querying, grouping, classification, facet
// filtering, and per-kind (chart vs value) detail rendering stay server-side
// (store/changeset/filter and the /api/changesets* endpoints). This file:
//   - fetches Changesets from /api/changesets (backdrop, facet-filtered)
//   - renders one flag per Changeset on a single time track with a dated axis
//   - groups the server-rendered facet controls into per-facet dropdowns, each
//     value cycling off -> include -> exclude, plus an "only" shortcut
//   - lets the user select a From/To window (drag-brush on the track or the two
//     datetime inputs) that filters the feed and dims out-of-range flags
//   - renders the "Changes" feed: repo short-name, linked commit, author, a
//     day/time stamp and change count — with explicit loading / empty states
//   - on a flag (or feed row) click, fetches the server-rendered detail HTML
//     and, for each chart-kind Change, its chart diff — which is re-rendered
//     client-side as a collapsed, color-coded (red/green) hunk view
//
// Security posture is unchanged: the only innerHTML/insertAdjacentHTML writes
// are the server-rendered, already-escaped detail panel and chart-diff slots.
// Every client-built string (feed rows, labels, the re-rendered diff hunks,
// loading/empty copy) is assigned via textContent, never concatenated into an
// HTML string.

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

  var CLUSTER_PIXEL_RADIUS = 10;

  var MIN_WINDOW_MS = 60 * 60 * 1000; // 1 hour
  var MAX_WINDOW_MS = 365 * 24 * 60 * 60 * 1000; // ~1 year
  var DEFAULT_WINDOW_MS = 14 * 24 * 60 * 60 * 1000; // 2 weeks
  var ZOOM_STEP = 1.4;

  // Backdrop page size. The feed is rendered client-side from this one set, so
  // request the server-side max in a single page.
  var BACKDROP_LIMIT = 100;

  // Diff hunking: lines of unchanged context kept on each side of a change.
  var DIFF_CONTEXT = 3;

  var AXIS_TICKS = 6;
  var MS_PER_DAY = 24 * 60 * 60 * 1000;
  var MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  var FACET_STATE_CYCLE = ['off', 'include', 'exclude'];

  var root = null;
  var svg = null;
  var trackWidth = 900;
  var trackHeight = 120;
  var trackMidY = 55; // track line y; axis labels sit below it

  var state = {
    windowEnd: Date.now(),
    windowMs: DEFAULT_WINDOW_MS,
    from: null, // selected range start (epoch ms) or null (open)
    to: null,   // selected range end (epoch ms) or null (open)
    changesets: [],
    loaded: false,
    hasFitWindow: false,
    rangeSelectMode: false
  };

  var facetState = {};   // facetState[facet][value] = 'include' | 'exclude'
  var facetValues = {};  // facet -> [values] (from server-rendered controls)

  // DOM handles resolved in init().
  var feedEls = { list: null, empty: null, title: null, count: null };
  var rangeEls = { toggle: null, from: null, to: null, clear: null, readout: null };

  var brush = { active: false, x0: 0, x1: 0 };

  // ---- small formatting helpers ----
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
    if (!sha || !/^https?:\/\//.test(repo)) {
      return '';
    }
    return repo.replace(/\/+$/, '').replace(/\.git$/, '') + '/commit/' + sha;
  }

  function repoColor(repo) {
    return REPO_COLORS[repo] || REPO_COLORS[repoShortName(repo)] || DEFAULT_COLOR;
  }

  function windowStart() { return state.windowEnd - state.windowMs; }
  function xForTime(t) { return ((t - windowStart()) / state.windowMs) * trackWidth; }
  function timeForX(px) { return windowStart() + (px / trackWidth) * state.windowMs; }

  // ---- facet filter params ----
  function cycleFacetState(facet, value) {
    var current = (facetState[facet] && facetState[facet][value]) || 'off';
    var next = FACET_STATE_CYCLE[(FACET_STATE_CYCLE.indexOf(current) + 1) % FACET_STATE_CYCLE.length];
    setFacetState(facet, value, next);
    return next;
  }

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

  function getFacetState(facet, value) {
    return (facetState[facet] && facetState[facet][value]) || 'off';
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
    return pairs.map(function (p) {
      return encodeURIComponent(p[0]) + '=' + encodeURIComponent(p[1]);
    }).join('&');
  }

  function activeFilterCount() {
    var n = 0;
    for (var f in facetState) {
      if (Object.prototype.hasOwnProperty.call(facetState, f)) { n += Object.keys(facetState[f]).length; }
    }
    return n;
  }

  // ---- backdrop fetch ----
  function fetchBackdrop(onDone) {
    var pairs = buildFilterParams();
    pairs.push(['limit', String(BACKDROP_LIMIT)]);
    var url = API_PATH + '?' + buildQueryString(pairs);
    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onload = function () {
      if (xhr.status !== 200) { onDone([]); return; }
      try { onDone((JSON.parse(xhr.responseText).changesets) || []); }
      catch (e) { onDone([]); }
    };
    xhr.onerror = function () { onDone([]); };
    xhr.send();
  }

  function svgEl(name, attrs) {
    var el = document.createElementNS('http://www.w3.org/2000/svg', name);
    for (var k in attrs) {
      if (Object.prototype.hasOwnProperty.call(attrs, k)) { el.setAttribute(k, attrs[k]); }
    }
    return el;
  }

  function clusterFlags(changesets) {
    var withX = changesets
      .map(function (cs) { return { cs: cs, x: xForTime(new Date(cs.committedAt).getTime()) }; })
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

  // ---- range filtering ----
  function hasRange() { return state.from !== null && state.to !== null; }

  function inRange(cs) {
    var t = new Date(cs.committedAt).getTime();
    if (state.from !== null && t < state.from) { return false; }
    if (state.to !== null && t > state.to) { return false; }
    return true;
  }

  // ---- detail panel + chart diff ----
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

  function fetchChangesetDetail(repo, commitSha, onDone) {
    fetchFragment(
      DETAIL_API_PATH + '?repo=' + encodeURIComponent(repo) + '&commitSha=' + encodeURIComponent(commitSha),
      onDone);
  }

  function fetchChartDiff(repo, commitSha, tenantPath, onDone) {
    fetchFragment(
      CHART_DIFF_API_PATH + '?repo=' + encodeURIComponent(repo) +
      '&commitSha=' + encodeURIComponent(commitSha) + '&path=' + encodeURIComponent(tenantPath),
      onDone);
  }

  // classifyDiffLine returns 'add' | 'del' | 'ctx' from a unified-diff line's
  // leading character (+ / - / space).
  function classifyDiffLine(line) {
    var c = line.charAt(0);
    if (c === '+') { return 'add'; }
    if (c === '-') { return 'del'; }
    return 'ctx';
  }

  // buildHunks collapses long runs of unchanged context into a single "gap"
  // marker, keeping DIFF_CONTEXT lines on each side of every change. Whole
  // added/removed manifests (all +/- lines) are left intact.
  function buildHunks(lines) {
    var rows = lines.map(function (l) { return { t: classifyDiffLine(l), text: l }; });
    var out = [];
    var i = 0;
    while (i < rows.length) {
      if (rows[i].t !== 'ctx') { out.push(rows[i]); i++; continue; }
      var j = i;
      while (j < rows.length && rows[j].t === 'ctx') { j++; }
      var runLen = j - i;
      var atStart = out.length === 0;
      var atEnd = j >= rows.length;
      var keepLead = atStart ? 0 : DIFF_CONTEXT;
      var keepTrail = atEnd ? 0 : DIFF_CONTEXT;
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

  // transformChartDiff replaces a chart-diff slot's raw <pre> unified diff with
  // a collapsed, color-coded hunk view built entirely from textContent.
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
        var tenantPath = slot.getAttribute('data-tenant-path') || '';
        slot.textContent = 'Rendering diff…';
        fetchChartDiff(cs.repo, cs.commitSha, tenantPath, function (html) {
          if (gen !== clickGeneration) { return; }
          if (html) {
            slot.innerHTML = html;
            transformChartDiff(slot);
          } else {
            slot.textContent = 'Could not load diff.';
          }
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

  // ---- timeline render ----
  function render() {
    if (!svg) { return; }
    while (svg.firstChild) { svg.removeChild(svg.firstChild); }

    // Selected / in-progress range band (drawn under everything else).
    var bandStart = null, bandEnd = null;
    if (brush.active) {
      bandStart = Math.min(brush.x0, brush.x1);
      bandEnd = Math.max(brush.x0, brush.x1);
    } else if (hasRange()) {
      bandStart = xForTime(state.from);
      bandEnd = xForTime(state.to);
    }
    if (bandStart !== null) {
      svg.appendChild(svgEl('rect', {
        x: Math.max(0, bandStart), y: 0,
        width: Math.max(0, Math.min(trackWidth, bandEnd) - Math.max(0, bandStart)), height: trackHeight,
        fill: '#0d6efd', opacity: 0.10
      }));
      [bandStart, bandEnd].forEach(function (bx) {
        svg.appendChild(svgEl('line', {
          x1: bx, y1: 0, x2: bx, y2: trackHeight, stroke: '#0d6efd', 'stroke-width': 1.5
        }));
      });
    }

    // Axis: track line + dated ticks.
    svg.appendChild(svgEl('line', {
      x1: 0, y1: trackMidY, x2: trackWidth, y2: trackMidY, stroke: '#dee2e6', 'stroke-width': 2
    }));
    for (var i = 0; i <= AXIS_TICKS; i++) {
      var tx = (i / AXIS_TICKS) * trackWidth;
      var tt = timeForX(tx);
      svg.appendChild(svgEl('line', {
        x1: tx, y1: trackMidY - 5, x2: tx, y2: trackMidY + 5, stroke: '#ced4da', 'stroke-width': 1
      }));
      var label = svgEl('text', {
        x: Math.min(Math.max(tx, 2), trackWidth - 2), y: trackMidY + 22,
        'text-anchor': i === 0 ? 'start' : (i === AXIS_TICKS ? 'end' : 'middle'),
        'font-size': 10, fill: '#868e96'
      });
      label.textContent = fmtTick(tt);
      svg.appendChild(label);
    }

    // Flags.
    clusterFlags(state.changesets).forEach(function (cluster) {
      var dimmed = hasRange() && cluster.members.every(function (cs) { return !inRange(cs); });
      if (cluster.members.length === 1) {
        var cs = cluster.members[0];
        var circle = svgEl('circle', {
          cx: cluster.x, cy: trackMidY, r: 6, fill: repoColor(cs.repo),
          opacity: dimmed ? 0.25 : 1, cursor: 'pointer', 'data-commit-sha': cs.commitSha
        });
        circle.addEventListener('click', function () { onFlagClick([cs]); });
        svg.appendChild(circle);
      } else {
        var group = svgEl('g', { cursor: 'pointer' });
        group.appendChild(svgEl('circle', {
          cx: cluster.x, cy: trackMidY, r: 10, fill: '#495057', opacity: dimmed ? 0.25 : 1
        }));
        var count = svgEl('text', {
          x: cluster.x, y: trackMidY + 4, 'text-anchor': 'middle', 'font-size': 10, fill: '#fff'
        });
        count.textContent = String(cluster.members.length);
        group.appendChild(count);
        group.addEventListener('click', function () { onFlagClick(cluster.members); });
        svg.appendChild(group);
      }
    });
  }

  function fitWindowToData(changesets) {
    if (state.hasFitWindow || changesets.length === 0) { return; }
    state.hasFitWindow = true;
    var oldest = changesets.reduce(function (min, cs) {
      var t = new Date(cs.committedAt).getTime();
      return t < min ? t : min;
    }, Infinity);
    if (!isFinite(oldest)) { return; }
    var span = (state.windowEnd - oldest) * 1.15; // a little padding on each edge
    if (span < MIN_WINDOW_MS) { span = MIN_WINDOW_MS; }
    if (span > MAX_WINDOW_MS) { span = MAX_WINDOW_MS; }
    state.windowMs = span;
  }

  function renderBackdrop(changesets) {
    state.changesets = changesets;
    state.loaded = true;
    fitWindowToData(changesets);
    render();
    renderFeed();
  }

  function loadBackdrop() {
    state.loaded = false;
    renderFeed();
    fetchBackdrop(renderBackdrop);
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
      btn.textContent = 'Clear filters & range';
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
    time.textContent = fmtDateTime(new Date(cs.committedAt).getTime());
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
      a.href = url;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      a.textContent = sha;
      a.title = cs.commitSha;
      a.addEventListener('click', function (e) { e.stopPropagation(); });
      li.appendChild(a);
    } else {
      var shaEl = document.createElement('span');
      shaEl.className = 'feed-commit feed-commit-plain';
      shaEl.textContent = sha;
      shaEl.title = cs.commitSha;
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

    if (feedEls.title) {
      feedEls.title.textContent = hasRange()
        ? 'Changes — ' + fmtDateTime(state.from) + ' → ' + fmtDateTime(state.to)
        : 'Changes';
    }

    if (!state.loaded) {
      if (feedEls.count) { feedEls.count.textContent = ''; }
      showEmpty('Loading changes…', false);
      return;
    }

    if (state.changesets.length === 0) {
      if (feedEls.count) { feedEls.count.textContent = ''; }
      showEmpty('No changes recorded yet — the poller may still be backfilling.', activeFilterCount() > 0);
      return;
    }

    var filtered = state.changesets.filter(inRange).sort(function (a, b) {
      return new Date(b.committedAt).getTime() - new Date(a.committedAt).getTime();
    });

    if (feedEls.count) {
      feedEls.count.textContent = filtered.length + ' of ' + state.changesets.length;
    }

    if (filtered.length === 0) {
      showEmpty('No changes match the current filters.', true);
      return;
    }

    hideEmpty();
    filtered.forEach(function (cs) { feedEls.list.appendChild(buildFeedRow(cs)); });
  }

  // ---- range controls ----
  function setRange(fromMs, toMs) {
    state.from = fromMs;
    state.to = toMs;
    syncRangeInputs();
    render();
    renderFeed();
  }

  function syncRangeInputs() {
    if (rangeEls.from) { rangeEls.from.value = state.from === null ? '' : toLocalInput(state.from); }
    if (rangeEls.to) { rangeEls.to.value = state.to === null ? '' : toLocalInput(state.to); }
    if (rangeEls.clear) { rangeEls.clear.disabled = !hasRange(); }
  }

  function clearRange() { setRange(null, null); }

  function clearAllFilters() {
    facetState = {};
    refreshAllFacetPills();
    clearRange();
    onFilterChanged();
  }

  function setRangeSelectMode(on) {
    state.rangeSelectMode = on;
    if (rangeEls.toggle) {
      rangeEls.toggle.setAttribute('data-active', on ? 'true' : 'false');
      rangeEls.toggle.textContent = on ? 'Selecting range…' : 'Select range';
    }
    if (svg) { svg.style.cursor = on ? 'crosshair' : 'grab'; }
  }

  // ---- facet dropdowns ----
  function collectServerFacets(container) {
    facetValues = {};
    var btns = container.querySelectorAll('[data-facet][data-value]');
    for (var i = 0; i < btns.length; i++) {
      var f = btns[i].getAttribute('data-facet');
      var v = btns[i].getAttribute('data-value');
      if (!facetValues[f]) { facetValues[f] = []; }
      if (facetValues[f].indexOf(v) < 0) { facetValues[f].push(v); }
    }
  }

  var pillEls = {}; // facet -> value -> pill element (to refresh on "only"/clear)

  function refreshAllFacetPills() {
    for (var f in pillEls) {
      if (!Object.prototype.hasOwnProperty.call(pillEls, f)) { continue; }
      for (var v in pillEls[f]) {
        if (!Object.prototype.hasOwnProperty.call(pillEls[f], v)) { continue; }
        pillEls[f][v].setAttribute('data-state', getFacetState(f, v));
      }
      refreshFacetBadge(f);
    }
  }

  var badgeEls = {};
  function refreshFacetBadge(facet) {
    var badge = badgeEls[facet];
    if (!badge) { return; }
    var n = facetState[facet] ? Object.keys(facetState[facet]).length : 0;
    badge.textContent = n ? String(n) : '';
    badge.style.display = n ? '' : 'none';
  }

  function buildFacetDropdowns(container) {
    collectServerFacets(container);
    container.innerHTML = '';
    container.classList.add('facet-dropdowns');

    var names = Object.keys(facetValues).sort();
    names.forEach(function (facet) {
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
          var next = cycleFacetState(facet, value);
          pill.setAttribute('data-state', next);
          refreshFacetBadge(facet);
          onFilterChanged();
        });
        pillEls[facet][value] = pill;
        rowEl.appendChild(pill);

        var only = document.createElement('button');
        only.type = 'button';
        only.className = 'facet-only';
        only.textContent = 'only';
        only.title = 'Include only this value';
        only.addEventListener('click', function () {
          facetValues[facet].forEach(function (other) {
            setFacetState(facet, other, other === value ? 'include' : 'exclude');
          });
          for (var v in pillEls[facet]) {
            if (Object.prototype.hasOwnProperty.call(pillEls[facet], v)) {
              pillEls[facet][v].setAttribute('data-state', getFacetState(facet, v));
            }
          }
          refreshFacetBadge(facet);
          onFilterChanged();
        });
        rowEl.appendChild(only);

        body.appendChild(rowEl);
      });

      dd.appendChild(body);
      container.appendChild(dd);
    });
  }

  // ---- interactions ----
  function zoom(factor) {
    var w = state.windowMs / factor;
    state.windowMs = Math.max(MIN_WINDOW_MS, Math.min(MAX_WINDOW_MS, w));
    render();
  }

  function pan(deltaMs) { state.windowEnd += deltaMs; render(); }

  var drag = { active: false, lastClientX: 0 };

  function svgX(clientX) { return clientX - svg.getBoundingClientRect().left; }

  function attachInteractions() {
    if (!svg) { return; }

    svg.addEventListener('wheel', function (evt) {
      evt.preventDefault();
      zoom(evt.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP);
    });

    svg.addEventListener('mousedown', function (evt) {
      if (state.rangeSelectMode) {
        brush.active = true;
        brush.x0 = brush.x1 = svgX(evt.clientX);
        render();
      } else {
        drag.active = true;
        drag.lastClientX = evt.clientX;
        svg.style.cursor = 'grabbing';
      }
    });

    window.addEventListener('mousemove', function (evt) {
      if (brush.active) {
        brush.x1 = svgX(evt.clientX);
        render();
      } else if (drag.active) {
        var deltaPx = evt.clientX - drag.lastClientX;
        drag.lastClientX = evt.clientX;
        pan(-deltaPx * (state.windowMs / trackWidth));
      }
    });

    window.addEventListener('mouseup', function () {
      if (brush.active) {
        brush.active = false;
        var a = timeForX(Math.min(brush.x0, brush.x1));
        var b = timeForX(Math.max(brush.x0, brush.x1));
        if (Math.abs(brush.x1 - brush.x0) < 4) {
          clearRange(); // a click, not a drag: clear any range
        } else {
          setRange(a, b);
        }
      }
      if (drag.active) {
        drag.active = false;
        svg.style.cursor = state.rangeSelectMode ? 'crosshair' : 'grab';
      }
    });
  }

  function onFilterChanged() {
    loadBackdrop(); // re-fetch backdrop with new facet params; feed re-renders on load
  }

  // ---- init ----
  function buildControls() {
    var controls = document.createElement('div');
    controls.className = 'timeline-controls';

    var toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'range-toggle';
    toggle.setAttribute('data-active', 'false');
    toggle.textContent = 'Select range';
    toggle.addEventListener('click', function () { setRangeSelectMode(!state.rangeSelectMode); });
    rangeEls.toggle = toggle;
    controls.appendChild(toggle);

    controls.appendChild(makeRangeInput('From', function (el) { rangeEls.from = el; }));
    controls.appendChild(makeRangeInput('To', function (el) { rangeEls.to = el; }));

    var clear = document.createElement('button');
    clear.type = 'button';
    clear.className = 'range-clear';
    clear.textContent = 'Clear range';
    clear.disabled = true;
    clear.addEventListener('click', clearRange);
    rangeEls.clear = clear;
    controls.appendChild(clear);

    var hint = document.createElement('span');
    hint.className = 'timeline-hint';
    hint.textContent = 'Scroll to zoom · drag to pan · “Select range” then drag to filter';
    controls.appendChild(hint);

    return controls;
  }

  function makeRangeInput(labelText, capture) {
    var label = document.createElement('label');
    label.className = 'range-label';
    label.textContent = labelText + ' ';
    var input = document.createElement('input');
    input.type = 'datetime-local';
    input.addEventListener('change', function () {
      var from = rangeEls.from && rangeEls.from.value ? Date.parse(rangeEls.from.value) : null;
      var to = rangeEls.to && rangeEls.to.value ? Date.parse(rangeEls.to.value) : null;
      setRange(isNaN(from) ? null : from, isNaN(to) ? null : to);
    });
    label.appendChild(input);
    capture(input);
    return label;
  }

  function init() {
    root = document.getElementById('timeline-root');
    if (!root) { return; }

    trackWidth = Math.max(600, (root.clientWidth || 900));

    root.appendChild(buildControls());

    svg = svgEl('svg', {
      width: trackWidth, height: trackHeight, viewBox: '0 0 ' + trackWidth + ' ' + trackHeight,
      'class': 'timeline-svg'
    });
    svg.style.cursor = 'grab';
    root.appendChild(svg);

    attachInteractions();

    var facetContainer = document.getElementById('facet-controls');
    if (facetContainer) { buildFacetDropdowns(facetContainer); }

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
