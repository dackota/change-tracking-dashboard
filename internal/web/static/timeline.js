// timeline.js — vendored, embedded (go:embed) client-side rendering for the
// Change Tracking Dashboard timeline. This is the dashboard's only
// client-side script; it is deliberately kept thin. All querying, grouping,
// classification, filtering, and per-kind (chart vs value) rendering logic
// lives server-side (store/changeset/filter and the GET /api/changesets and
// GET /api/changesets/detail endpoints) — this file only:
//   - fetches Changesets from /api/changesets
//   - renders one flag per Changeset on a single time track, colored by repo
//   - manages the as-of marker (typed input + a visible drag handle), dimming
//     everything after it
//   - implements zoom/pan between a broad overview and a tight window
//   - clusters dense regions into a single counted marker that splits apart
//     on zoom-in
//   - cycles each server-rendered facet control through include -> exclude
//     -> off and assembles the resulting filter into request params
//   - renders the "Changes before T" panel: a most-recent-first, paginated
//     list of Changesets committed before the as-of marker, honoring the
//     active facet filters
//   - on a flag click, fetches the server-rendered detail HTML from
//     /api/changesets/detail (per Changeset clicked) and injects it into a
//     detail panel — no per-kind Changeset detail rendering happens here;
//     the server has already rendered and HTML-escaped everything, so this
//     is a plain innerHTML assignment of trusted, first-party markup
//   - once that detail HTML lands, wires each chart-kind Change's own
//     change-helm-diff-slot live: shows a "Rendering diff…" state, then
//     issues one independent fetch per chart-kind Change to
//     /api/changesets/detail/chart-diff and injects the returned HTML (also
//     trusted, server-escaped, first-party markup) into that specific slot
//
// The as-of marker is a pure client-side view concern, decoupled from what
// data is loaded for the backdrop: the backdrop (the full set of Changesets
// rendered on the track) is fetched independently of the marker position —
// always up to "now" (the endpoint's own asOf-omitted default) — so it
// always spans both sides of an incident marker sitting somewhere in the
// past. /api/changesets' `committedAt < asOf` semantics are for the
// "Changes before T" panel below, not for this backdrop: moving or typing
// the marker only changes which flags render dimmed and what the panel
// fetches — it never changes what the backdrop fetches. The active facet
// filters, by contrast, drive BOTH the backdrop and the panel: changing a
// facet control re-fetches both.
//
// No external CDN, no network fetch other than this page's own
// /api/changesets, /api/changesets/detail, and
// /api/changesets/detail/chart-diff endpoints. Facet/label text and the
// "Changes before T" panel rows are inserted via textContent (never parsed
// as HTML); the only innerHTML/insertAdjacentHTML writes are the detail
// panel and each chart-kind Change's helm-diff slot, both of which inject
// only trusted, already-escaped server-rendered markup (from
// /api/changesets/detail and /api/changesets/detail/chart-diff
// respectively) — any client-built text (e.g. the "Rendering diff…" /
// failure states below) is always set via textContent, never concatenated
// into an HTML string.
//
// A flag click can be superseded by another before its fetches resolve — a
// module-scoped clickGeneration counter (see its own comment near
// onFlagClick) guards every async continuation reachable from a click so a
// stale callback from a superseded click never mutates the detail panel or
// fires a further request.

(function () {
  'use strict';

  var API_PATH = '/api/changesets';
  var DETAIL_API_PATH = '/api/changesets/detail';
  var CHART_DIFF_API_PATH = '/api/changesets/detail/chart-diff';

  // Repo -> color. Only the two repos named in the PRD are given fixed
  // colors; anything else falls back to a neutral color so an unexpected
  // repo name never throws.
  var REPO_COLORS = {
    'application-config': '#0d6efd',
    'infrastructure-config': '#fd7e14'
  };
  var DEFAULT_COLOR = '#6c757d';

  // Clustering: flags whose rendered x-position falls within this many
  // pixels of each other are collapsed into one counted cluster marker.
  var CLUSTER_PIXEL_RADIUS = 10;

  // Zoom bounds, expressed as the visible window width in milliseconds.
  // MIN is a tight few-hour window; MAX is a broad multi-week overview.
  var MIN_WINDOW_MS = 60 * 60 * 1000; // 1 hour
  var MAX_WINDOW_MS = 90 * 24 * 60 * 60 * 1000; // ~90 days
  var DEFAULT_WINDOW_MS = 14 * 24 * 60 * 60 * 1000; // 2 weeks
  var ZOOM_STEP = 1.4;

  // The as-of marker's visible drag handle: a circle near the top of the
  // marker line. This is the discoverable affordance for dragging the
  // marker (story 2) — no hidden modifier key required.
  var MARKER_HANDLE_Y = 10;
  var MARKER_HANDLE_RADIUS = 7;

  // The tri-state cycle every facet control walks through on each click, in
  // order. 'off' means no filter is applied for that facet/value pair.
  var FACET_STATE_CYCLE = ['off', 'include', 'exclude'];

  // Page size requested per "Changes before T" panel fetch (both the initial
  // load and each "Load more" click).
  var CHANGES_BEFORE_PAGE_LIMIT = 25;

  var root = null;
  var svg = null;
  var trackWidth = 800;
  var trackHeight = 90;

  // View state: [windowStart, windowEnd] in epoch ms, and asOf (epoch ms or
  // null when unset — unset means "now"). hasFitWindow guards the one-time
  // "fit window to loaded data" adjustment on first load.
  var state = {
    windowEnd: Date.now(),
    windowMs: DEFAULT_WINDOW_MS,
    asOf: null,
    changesets: [],
    hasFitWindow: false
  };

  // facetState[facet][value] = 'include' | 'exclude'. A facet/value pair
  // absent from its inner map is 'off' — the default for every
  // server-rendered control until the user clicks it.
  var facetState = {};

  // changesBeforePanel tracks the "Changes before T" list's own paging
  // cursor and DOM handles, independent of the backdrop's state above.
  var changesBeforePanel = {
    listEl: null,
    loadMoreBtn: null,
    nextCursor: '',
    loading: false
  };

  function repoColor(repo) {
    return REPO_COLORS[repo] || DEFAULT_COLOR;
  }

  function windowStart() {
    return state.windowEnd - state.windowMs;
  }

  // cycleFacetState advances the tri-state for one facet/value pair to the
  // next state in FACET_STATE_CYCLE, mutating facetState in place (pruning
  // the entry entirely once it cycles back to 'off' so buildFilterParams
  // never has to distinguish an explicit 'off' from an absent entry).
  function cycleFacetState(facet, value) {
    var current = (facetState[facet] && facetState[facet][value]) || 'off';
    var next = FACET_STATE_CYCLE[(FACET_STATE_CYCLE.indexOf(current) + 1) % FACET_STATE_CYCLE.length];

    if (next === 'off') {
      if (facetState[facet]) {
        delete facetState[facet][value];
        if (Object.keys(facetState[facet]).length === 0) {
          delete facetState[facet];
        }
      }
    } else {
      if (!facetState[facet]) {
        facetState[facet] = {};
      }
      facetState[facet][value] = next;
    }

    return next;
  }

  // buildFilterParams translates the active facetState into the
  // <facet>=<value> (include) / <facet>=-<value> (exclude) request params
  // the JSON endpoint already understands, as an array of [key, value]
  // pairs (rather than a pre-joined query string) so callers can append
  // additional params (asOf, cursor, limit) before serializing.
  function buildFilterParams() {
    var pairs = [];
    for (var facet in facetState) {
      if (!Object.prototype.hasOwnProperty.call(facetState, facet)) {
        continue;
      }
      var values = facetState[facet];
      for (var value in values) {
        if (!Object.prototype.hasOwnProperty.call(values, value)) {
          continue;
        }
        var encoded = values[value] === 'exclude' ? '-' + value : value;
        pairs.push([facet, encoded]);
      }
    }
    return pairs;
  }

  // buildQueryString joins an array of [key, value] pairs (as returned by
  // buildFilterParams, optionally extended with more pairs) into a URL query
  // string, percent-encoding each part.
  function buildQueryString(pairs) {
    return pairs
      .map(function (pair) {
        return encodeURIComponent(pair[0]) + '=' + encodeURIComponent(pair[1]);
      })
      .join('&');
  }

  // xForTime maps an epoch-ms timestamp to an x pixel coordinate within the
  // current [windowStart, windowEnd] view.
  function xForTime(t) {
    var start = windowStart();
    var frac = (t - start) / state.windowMs;
    return frac * trackWidth;
  }

  // fetchBackdrop calls the JSON endpoint for the timeline's backdrop — the
  // full set of Changesets to render on the track. It deliberately omits
  // asOf (defaulting to "now" server-side) so the result always spans both
  // sides of an as-of marker placed anywhere in the past; the marker never
  // drives this fetch (see the file-level comment). The active facet
  // filters DO drive this fetch — changing a control re-fetches the
  // backdrop with the current include/exclude params. The endpoint already
  // returns most-recent-first, page-capped results; the timeline renders
  // whatever the first page contains within the current viewport. This
  // keeps all query/pagination policy server-side.
  function fetchBackdrop(onDone) {
    var qs = buildQueryString(buildFilterParams());
    var url = qs ? API_PATH + '?' + qs : API_PATH;

    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onload = function () {
      if (xhr.status !== 200) {
        onDone([]);
        return;
      }
      try {
        var body = JSON.parse(xhr.responseText);
        onDone(body.changesets || []);
      } catch (e) {
        onDone([]);
      }
    };
    xhr.onerror = function () {
      onDone([]);
    };
    xhr.send();
  }

  function svgEl(name, attrs) {
    var el = document.createElementNS('http://www.w3.org/2000/svg', name);
    for (var k in attrs) {
      if (Object.prototype.hasOwnProperty.call(attrs, k)) {
        el.setAttribute(k, attrs[k]);
      }
    }
    return el;
  }

  // clusterFlags groups changesets whose rendered x position is within
  // CLUSTER_PIXEL_RADIUS of one another into clusters. A cluster of size 1
  // renders as a normal flag; a cluster of size > 1 renders as a single
  // counted marker that splits apart once zooming in spreads the members
  // beyond the pixel radius (handled naturally by re-clustering on every
  // render — no separate "split" state is kept).
  function clusterFlags(changesets) {
    var withX = changesets
      .map(function (cs) {
        return { cs: cs, x: xForTime(new Date(cs.committedAt).getTime()) };
      })
      .filter(function (item) {
        return item.x >= 0 && item.x <= trackWidth;
      })
      .sort(function (a, b) {
        return a.x - b.x;
      });

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

  // detailPanel holds the server-rendered HTML for the Changeset(s) behind
  // the most recently clicked flag/cluster. Created lazily on first click.
  var detailPanel = null;

  // clickGeneration guards against stale async callbacks from a superseded
  // flag click. onFlagClick bumps this counter and captures the resulting
  // value as its own generation; every async continuation it (transitively)
  // schedules — fetchChangesetDetail's onDone below, and fetchChartDiff's
  // onDone inside loadChartDiffsForChangeset — compares its captured
  // generation against the current clickGeneration before touching the DOM
  // or kicking off a further request. If the user has since clicked another
  // flag/cluster, clickGeneration has moved on, the comparison fails, and
  // the stale callback no-ops instead of appending orphaned detail HTML into
  // a panel that now belongs to a different click, firing an abandoned
  // chart-diff XHR, or writing into a slot that belongs to a different
  // Changeset's detail HTML.
  var clickGeneration = 0;

  function ensureDetailPanel() {
    if (!detailPanel && root) {
      detailPanel = document.createElement('div');
      detailPanel.id = 'timeline-detail-panel';
      root.appendChild(detailPanel);
    }
    return detailPanel;
  }

  // fetchChangesetDetail calls the server-rendered detail endpoint for one
  // Changeset (identified by repo + commitSha) and passes the raw HTML body
  // to onDone. All per-kind (chart vs value) dispatch/rendering already
  // happened server-side — this is a plain fetch, no client-side markup
  // construction.
  function fetchChangesetDetail(repo, commitSha, onDone) {
    var xhr = new XMLHttpRequest();
    var url =
      DETAIL_API_PATH +
      '?repo=' + encodeURIComponent(repo) +
      '&commitSha=' + encodeURIComponent(commitSha);
    xhr.open('GET', url, true);
    xhr.onload = function () {
      if (xhr.status !== 200) {
        onDone('');
        return;
      }
      onDone(xhr.responseText);
    };
    xhr.onerror = function () {
      onDone('');
    };
    xhr.send();
  }

  // fetchChartDiff calls the chart-diff endpoint for a single chart-kind
  // Change — identified by its Changeset's own repo + commitSha, plus the
  // tenant chart directory carried on that Change's own detail slot — and
  // passes the raw HTML fragment to onDone, or an empty string on any
  // non-200 response or XHR error (mirrors fetchChangesetDetail's XHR
  // pattern). Called once per chart-kind Change, never batched, so one
  // slow or failing render never blocks another's slot.
  function fetchChartDiff(repo, commitSha, tenantPath, onDone) {
    var xhr = new XMLHttpRequest();
    var url =
      CHART_DIFF_API_PATH +
      '?repo=' + encodeURIComponent(repo) +
      '&commitSha=' + encodeURIComponent(commitSha) +
      '&path=' + encodeURIComponent(tenantPath);
    xhr.open('GET', url, true);
    xhr.onload = function () {
      if (xhr.status !== 200) {
        onDone('');
        return;
      }
      onDone(xhr.responseText);
    };
    xhr.onerror = function () {
      onDone('');
    };
    xhr.send();
  }

  // loadChartDiffsForChangeset finds every chart-kind Change's helm-diff
  // slot within subtree (one Changeset's just-inserted detail HTML) and
  // wires each independently: the slot immediately shows a "Rendering
  // diff…" state via textContent (replacing the server's static
  // placeholder copy), then fetches that one Change's own Chart diff. On
  // success the returned HTML (already server-escaped, trusted, first-party
  // markup) is injected via innerHTML; on failure (non-200 or XHR error)
  // the slot shows a generic textContent message instead of being left
  // stuck on "Rendering diff…". Each slot's fetch is independent — a
  // slow/failing one never blocks another slot or the rest of the detail
  // view, which is already visible by the time these fetches start.
  // gen is the clickGeneration the caller (onFlagClick) fired under; every
  // fetchChartDiff onDone callback below re-checks it against the current
  // clickGeneration before mutating its slot, since a chart-diff XHR
  // legitimately started under gen can still resolve after a later click has
  // moved clickGeneration on (see the clickGeneration comment above).
  function loadChartDiffsForChangeset(subtree, cs, gen) {
    if (!subtree || !subtree.querySelectorAll) {
      return;
    }
    var slots = subtree.querySelectorAll('.change-helm-diff-slot');
    for (var i = 0; i < slots.length; i++) {
      (function (slot) {
        var tenantPath = slot.getAttribute('data-tenant-path') || '';
        slot.textContent = 'Rendering diff…';
        fetchChartDiff(cs.repo, cs.commitSha, tenantPath, function (html) {
          if (gen !== clickGeneration) {
            return;
          }
          if (html) {
            slot.innerHTML = html;
          } else {
            slot.textContent = 'Could not load diff.';
          }
        });
      })(slots[i]);
    }
  }

  // onFlagClick fetches the server-rendered detail view for every Changeset
  // behind the clicked flag/cluster and injects the resulting HTML into the
  // detail panel. The server (GET /api/changesets/detail) has already
  // dispatched each Change to its per-kind (chart vs value) rendering and
  // HTML-escaped every interpolated value, so assigning the response as
  // innerHTML here injects only trusted, first-party markup — never
  // untrusted Change data built into HTML client-side. Once a Changeset's
  // detail HTML is inserted, its chart-kind Changes' helm-diff slots (if
  // any) are wired live via loadChartDiffsForChangeset, scoped to just that
  // insertion (panel.lastElementChild) so a cluster of several Changesets
  // never mixes up which slot belongs to which.
  //
  // Clicking a flag/cluster while a prior click's fetches are still in
  // flight supersedes that prior click: this bumps clickGeneration and
  // captures the resulting value as gen. Every async callback below (and
  // every one loadChartDiffsForChangeset schedules under this same gen)
  // re-checks gen against clickGeneration before mutating anything, so a
  // stale callback from the superseded click no-ops instead of appending
  // orphaned HTML into a panel that now belongs to this new click, firing an
  // abandoned chart-diff request for the abandoned Changeset, or wiring a
  // stale slot into whatever panel.lastElementChild has since become.
  function onFlagClick(changesets) {
    var panel = ensureDetailPanel();
    if (!panel) {
      return;
    }
    clickGeneration++;
    var gen = clickGeneration;
    panel.innerHTML = '';
    changesets.forEach(function (cs) {
      fetchChangesetDetail(cs.repo, cs.commitSha, function (html) {
        if (gen !== clickGeneration) {
          return;
        }
        if (!html) {
          return;
        }
        panel.insertAdjacentHTML('beforeend', html);
        loadChartDiffsForChangeset(panel.lastElementChild, cs, gen);
      });
    });
  }

  function render() {
    if (!svg) {
      return;
    }
    while (svg.firstChild) {
      svg.removeChild(svg.firstChild);
    }

    // Track line.
    svg.appendChild(
      svgEl('line', {
        x1: 0,
        y1: trackHeight / 2,
        x2: trackWidth,
        y2: trackHeight / 2,
        stroke: '#dee2e6',
        'stroke-width': 2
      })
    );

    var clusters = clusterFlags(state.changesets);
    clusters.forEach(function (cluster) {
      var isDimmed =
        state.asOf !== null &&
        cluster.members.every(function (cs) {
          return new Date(cs.committedAt).getTime() >= state.asOf;
        });

      if (cluster.members.length === 1) {
        var cs = cluster.members[0];
        var circle = svgEl('circle', {
          cx: cluster.x,
          cy: trackHeight / 2,
          r: 6,
          fill: repoColor(cs.repo),
          opacity: isDimmed ? 0.25 : 1,
          'data-commit-sha': cs.commitSha
        });
        circle.addEventListener('click', function () {
          onFlagClick([cs]);
        });
        svg.appendChild(circle);
      } else {
        var group = svgEl('g', {});
        var clusterCircle = svgEl('circle', {
          cx: cluster.x,
          cy: trackHeight / 2,
          r: 10,
          fill: '#495057',
          opacity: isDimmed ? 0.25 : 1
        });
        var label = svgEl('text', {
          x: cluster.x,
          y: trackHeight / 2 + 4,
          'text-anchor': 'middle',
          'font-size': 10,
          fill: '#fff'
        });
        label.textContent = String(cluster.members.length);
        group.appendChild(clusterCircle);
        group.appendChild(label);
        group.addEventListener('click', function () {
          onFlagClick(cluster.members);
        });
        svg.appendChild(group);
      }
    });

    // As-of marker line + a visible, draggable handle. The handle (not a
    // hidden modifier key) is the discoverable affordance for story 2: drag
    // the handle to move the marker; dragging the empty track pans instead.
    if (state.asOf !== null) {
      var markerX = xForTime(state.asOf);
      svg.appendChild(
        svgEl('line', {
          x1: markerX,
          y1: 0,
          x2: markerX,
          y2: trackHeight,
          stroke: '#dc3545',
          'stroke-width': 2,
          'stroke-dasharray': '4,2'
        })
      );

      var handle = svgEl('circle', {
        cx: markerX,
        cy: MARKER_HANDLE_Y,
        r: MARKER_HANDLE_RADIUS,
        fill: '#dc3545',
        stroke: '#fff',
        'stroke-width': 1.5,
        cursor: 'ew-resize',
        'class': 'timeline-marker-handle'
      });
      handle.addEventListener('mousedown', function (evt) {
        evt.stopPropagation();
        beginMarkerDrag(evt.clientX);
      });
      svg.appendChild(handle);
    }
  }

  // fitWindowToData sizes the initial view window to the loaded data's own
  // span (oldest to newest committedAt, clamped to [MIN_WINDOW_MS,
  // MAX_WINDOW_MS] and to the current windowEnd) instead of always defaulting
  // to DEFAULT_WINDOW_MS. This avoids a mostly-blank track when the loaded
  // history is sparse or younger than two weeks. Only applied on first load
  // (state.hasFitWindow guards against re-fitting after the user has zoomed
  // or panned).
  function fitWindowToData(changesets) {
    if (state.hasFitWindow || changesets.length === 0) {
      return;
    }
    state.hasFitWindow = true;

    var oldest = changesets.reduce(function (min, cs) {
      var t = new Date(cs.committedAt).getTime();
      return t < min ? t : min;
    }, Infinity);
    if (!isFinite(oldest)) {
      return;
    }

    var span = state.windowEnd - oldest;
    if (span < MIN_WINDOW_MS) {
      span = MIN_WINDOW_MS;
    }
    if (span > MAX_WINDOW_MS) {
      span = MAX_WINDOW_MS;
    }
    state.windowMs = span;
  }

  function renderBackdrop(changesets) {
    fitWindowToData(changesets);
    state.changesets = changesets;
    render();
  }

  function loadBackdrop() {
    fetchBackdrop(renderBackdrop);
  }

  // setAsOf updates the marker position, re-renders the backdrop dimming,
  // and reloads the "Changes before T" panel from scratch for the new T.
  // It never refetches the backdrop itself: the backdrop is independent of
  // the marker (see the file-level comment), so moving/typing the marker
  // only changes which already-loaded flags render dimmed there.
  function setAsOf(epochMs) {
    state.asOf = epochMs;
    render();
    reloadChangesBeforePanel();
  }

  // formatChangesetSummary builds the plain-text label for one row of the
  // "Changes before T" panel: commit, repo, author, and change count. Always
  // assigned via textContent by the caller — never parsed as HTML.
  function formatChangesetSummary(cs) {
    var changeCount = (cs.changes || []).length;
    return (
      cs.committedAt +
      '  ' +
      cs.repo +
      '@' +
      cs.commitSha.slice(0, 8) +
      ' (' +
      cs.author +
      ') — ' +
      changeCount +
      (changeCount === 1 ? ' change' : ' changes')
    );
  }

  // appendChangesBeforeRows renders one <li> per Changeset, appended to the
  // panel's list in the order given (the endpoint already returns
  // most-recent-first, so no client-side sorting is needed). Text is set via
  // textContent so no Changeset field is ever parsed as HTML.
  function appendChangesBeforeRows(changesets) {
    if (!changesBeforePanel.listEl) {
      return;
    }
    changesets.forEach(function (cs) {
      var li = document.createElement('li');
      li.textContent = formatChangesetSummary(cs);
      changesBeforePanel.listEl.appendChild(li);
    });
  }

  // fetchChangesBefore calls /api/changesets with asOf=T (the marker,
  // defaulting to "now" when unset) plus the active facet filters and the
  // given cursor — this is the one data path that DOES pass asOf, unlike
  // fetchBackdrop. limit bounds this page; the caller walks nextCursor to
  // page through the full, unbounded retained history.
  function fetchChangesBefore(cursor, onDone) {
    var asOfMs = state.asOf === null ? Date.now() : state.asOf;
    var pairs = buildFilterParams();
    pairs.push(['asOf', new Date(asOfMs).toISOString()]);
    pairs.push(['limit', String(CHANGES_BEFORE_PAGE_LIMIT)]);
    if (cursor) {
      pairs.push(['cursor', cursor]);
    }
    var url = API_PATH + '?' + buildQueryString(pairs);

    var xhr = new XMLHttpRequest();
    xhr.open('GET', url, true);
    xhr.onload = function () {
      if (xhr.status !== 200) {
        onDone({ changesets: [], nextCursor: '' });
        return;
      }
      try {
        var body = JSON.parse(xhr.responseText);
        onDone({ changesets: body.changesets || [], nextCursor: body.nextCursor || '' });
      } catch (e) {
        onDone({ changesets: [], nextCursor: '' });
      }
    };
    xhr.onerror = function () {
      onDone({ changesets: [], nextCursor: '' });
    };
    xhr.send();
  }

  // updateLoadMoreVisibility shows/hides the panel's "Load more" button
  // based on whether a further page is available.
  function updateLoadMoreVisibility() {
    if (!changesBeforePanel.loadMoreBtn) {
      return;
    }
    changesBeforePanel.loadMoreBtn.style.display = changesBeforePanel.nextCursor ? '' : 'none';
  }

  // loadMoreChangesBefore fetches and appends the next page of the
  // "Changes before T" panel using the stored cursor. Guards against
  // overlapping requests (e.g. a double-click) with the loading flag.
  function loadMoreChangesBefore() {
    if (changesBeforePanel.loading || !changesBeforePanel.nextCursor) {
      return;
    }
    changesBeforePanel.loading = true;
    fetchChangesBefore(changesBeforePanel.nextCursor, function (page) {
      changesBeforePanel.loading = false;
      appendChangesBeforeRows(page.changesets);
      changesBeforePanel.nextCursor = page.nextCursor;
      updateLoadMoreVisibility();
    });
  }

  // reloadChangesBeforePanel clears the panel and fetches the first page
  // from scratch — called whenever T (the as-of marker) or the active facet
  // filters change, since both affect which Changesets belong in the list.
  function reloadChangesBeforePanel() {
    if (!changesBeforePanel.listEl) {
      return;
    }
    while (changesBeforePanel.listEl.firstChild) {
      changesBeforePanel.listEl.removeChild(changesBeforePanel.listEl.firstChild);
    }
    changesBeforePanel.nextCursor = '';
    changesBeforePanel.loading = true;
    fetchChangesBefore('', function (page) {
      changesBeforePanel.loading = false;
      appendChangesBeforeRows(page.changesets);
      changesBeforePanel.nextCursor = page.nextCursor;
      updateLoadMoreVisibility();
    });
  }

  function zoom(factor, pivotClientX) {
    var newWindowMs = state.windowMs / factor;
    if (newWindowMs < MIN_WINDOW_MS) {
      newWindowMs = MIN_WINDOW_MS;
    }
    if (newWindowMs > MAX_WINDOW_MS) {
      newWindowMs = MAX_WINDOW_MS;
    }
    state.windowMs = newWindowMs;
    render();
  }

  function pan(deltaMs) {
    state.windowEnd += deltaMs;
    render();
  }

  // drag tracks the in-progress pointer drag, if any. mode is 'marker' when
  // the user grabbed the marker's visible handle, or 'pan' when they grabbed
  // the empty track — the handle is the only way to reach 'marker' mode, so
  // dragging the marker is always an explicit, visible interaction rather
  // than a hidden modifier-key gesture.
  var drag = { active: false, mode: null, lastClientX: 0 };

  // beginMarkerDrag starts a marker-drag from the handle's own mousedown.
  // Called with evt.stopPropagation() already applied by the caller so the
  // track's own mousedown (which would start a pan) never also fires.
  function beginMarkerDrag(clientX) {
    drag.active = true;
    drag.mode = 'marker';
    drag.lastClientX = clientX;
  }

  function beginPanDrag(clientX) {
    drag.active = true;
    drag.mode = 'pan';
    drag.lastClientX = clientX;
  }

  function endDrag() {
    drag.active = false;
    drag.mode = null;
  }

  function continueDrag(clientX) {
    if (!drag.active) {
      return;
    }
    var deltaPx = clientX - drag.lastClientX;
    drag.lastClientX = clientX;
    var msPerPixel = state.windowMs / trackWidth;
    if (drag.mode === 'marker') {
      var current = state.asOf === null ? state.windowEnd : state.asOf;
      setAsOf(current + deltaPx * msPerPixel);
    } else {
      pan(-deltaPx * msPerPixel);
    }
  }

  function attachInteractions(container, timestampInput) {
    if (timestampInput) {
      timestampInput.addEventListener('change', function () {
        var parsed = Date.parse(timestampInput.value);
        if (!isNaN(parsed)) {
          setAsOf(parsed);
        }
      });
    }

    if (!svg) {
      return;
    }

    svg.addEventListener('wheel', function (evt) {
      evt.preventDefault();
      var factor = evt.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP;
      zoom(factor);
    });

    // Dragging the empty track always pans — moving the marker requires
    // grabbing its visible handle (wired in render() via beginMarkerDrag),
    // which stops propagation so this handler never also starts a pan.
    svg.addEventListener('mousedown', function (evt) {
      beginPanDrag(evt.clientX);
    });
    window.addEventListener('mouseup', endDrag);
    window.addEventListener('mousemove', function (evt) {
      continueDrag(evt.clientX);
    });
  }

  // onFilterChanged is called whenever a facet control's tri-state changes.
  // Both the backdrop and the "Changes before T" panel are driven by the
  // active filter set, so both are re-fetched.
  function onFilterChanged() {
    loadBackdrop();
    reloadChangesBeforePanel();
  }

  // attachFacetControlInteractions wires click-to-cycle behavior onto every
  // server-rendered facet control (button[data-facet][data-value]) found
  // within container. Each click advances that control's tri-state, updates
  // its data-state attribute (which the embedded stylesheet uses to color
  // the include/exclude states), and re-fetches both data paths that the
  // active filter set drives.
  function attachFacetControlInteractions(container) {
    var controls = container.querySelectorAll('[data-facet][data-value]');
    for (var i = 0; i < controls.length; i++) {
      (function (btn) {
        btn.addEventListener('click', function () {
          var next = cycleFacetState(btn.getAttribute('data-facet'), btn.getAttribute('data-value'));
          btn.setAttribute('data-state', next);
          onFilterChanged();
        });
      })(controls[i]);
    }
  }

  // attachChangesBeforePanel locates the panel's list and "Load more"
  // elements (rendered by the server-side shell) and wires the button.
  function attachChangesBeforePanel() {
    changesBeforePanel.listEl = document.getElementById('changes-before-list');
    changesBeforePanel.loadMoreBtn = document.getElementById('changes-before-load-more');
    if (changesBeforePanel.loadMoreBtn) {
      changesBeforePanel.loadMoreBtn.addEventListener('click', loadMoreChangesBefore);
    }
  }

  function init() {
    root = document.getElementById('timeline-root');
    if (!root) {
      return;
    }

    var controls = document.createElement('div');
    controls.className = 'timeline-controls';

    var label = document.createElement('label');
    label.textContent = 'As of: ';
    var input = document.createElement('input');
    input.type = 'datetime-local';
    input.id = 'timeline-asof';
    label.appendChild(input);
    controls.appendChild(label);

    var hint = document.createElement('span');
    hint.className = 'timeline-hint';
    hint.textContent = 'Drag the red handle on the timeline to move the marker.';
    controls.appendChild(hint);

    root.appendChild(controls);

    svg = svgEl('svg', {
      width: trackWidth,
      height: trackHeight,
      viewBox: '0 0 ' + trackWidth + ' ' + trackHeight
    });
    root.appendChild(svg);

    attachInteractions(root, input);

    var facetControlsContainer = document.getElementById('facet-controls');
    if (facetControlsContainer) {
      attachFacetControlInteractions(facetControlsContainer);
    }
    attachChangesBeforePanel();

    loadBackdrop();
    reloadChangesBeforePanel();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
