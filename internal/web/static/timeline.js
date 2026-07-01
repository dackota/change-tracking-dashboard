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
//   - on a flag click, fetches the server-rendered detail HTML from
//     /api/changesets/detail (per Changeset clicked) and injects it into a
//     detail panel — no per-kind Changeset detail rendering happens here;
//     the server has already rendered and HTML-escaped everything, so this
//     is a plain innerHTML assignment of trusted, first-party markup
//
// The as-of marker is a pure client-side view concern, decoupled from what
// data is loaded: the backdrop (the full set of Changesets rendered on the
// track) is fetched independently of the marker position — always up to
// "now" (the endpoint's own asOf-omitted default) — so it always spans both
// sides of an incident marker sitting somewhere in the past. /api/changesets'
// `committedAt < asOf` semantics are for the downstream "Changes before T"
// list slice, not for this backdrop: moving or typing the marker here only
// changes which flags render dimmed, never what is fetched.
//
// No external CDN, no network fetch other than this page's own
// /api/changesets and /api/changesets/detail endpoints.

(function () {
  'use strict';

  var API_PATH = '/api/changesets';
  var DETAIL_API_PATH = '/api/changesets/detail';

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

  function repoColor(repo) {
    return REPO_COLORS[repo] || DEFAULT_COLOR;
  }

  function windowStart() {
    return state.windowEnd - state.windowMs;
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
  // drives this fetch (see the file-level comment). The endpoint already
  // returns most-recent-first, page-capped results; the timeline renders
  // whatever the first page contains within the current viewport. This
  // keeps all query/pagination policy server-side.
  function fetchBackdrop(onDone) {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', API_PATH, true);
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

  // onFlagClick fetches the server-rendered detail view for every Changeset
  // behind the clicked flag/cluster and injects the resulting HTML into the
  // detail panel. The server (GET /api/changesets/detail) has already
  // dispatched each Change to its per-kind (chart vs value) rendering and
  // HTML-escaped every interpolated value, so assigning the response as
  // innerHTML here injects only trusted, first-party markup — never
  // untrusted Change data built into HTML client-side.
  function onFlagClick(changesets) {
    var panel = ensureDetailPanel();
    if (!panel) {
      return;
    }
    panel.innerHTML = '';
    changesets.forEach(function (cs) {
      fetchChangesetDetail(cs.repo, cs.commitSha, function (html) {
        if (html) {
          panel.insertAdjacentHTML('beforeend', html);
        }
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

  // setAsOf updates the marker position and re-renders only. It never
  // refetches: the backdrop is independent of the marker (see the
  // file-level comment), so moving/typing the marker only changes which
  // already-loaded flags render dimmed.
  function setAsOf(epochMs) {
    state.asOf = epochMs;
    render();
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
    loadBackdrop();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
