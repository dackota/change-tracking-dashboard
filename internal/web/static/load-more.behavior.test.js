// load-more.behavior.test.js — regression coverage for two correctness-gate
// findings against timeline.js's loadMore() (R25's "Load more" affordance),
// neither of which is a pure-function property (they involve async
// state-machine timing), so they don't belong in
// feed-pagination.property.test.js alongside mergeChangesetPage's own
// property sweep.
//
// fetchChangesetsPage (and therefore loadMore) issues its request via
// XMLHttpRequest, not the fetch() API — this file stubs a controllable,
// manually-resolved global.XMLHttpRequest (open/send are no-ops; the test
// invokes the captured instance's onload itself, whenever it chooses to)
// rather than global.fetch, so the mock matches what the code under test
// actually calls.
//
// This is a plain Node script (no test framework, no new npm dependency),
// mirroring this directory's other *.test.js files' conventions. Run with:
//
//   node internal/web/static/load-more.behavior.test.js
//
// loadMore and state are exposed for this file only via the same CommonJS
// export hook at the bottom of timeline.js's IIFE used by commitURL/
// repoShortName/facetChips/mergeChangesetPage, gated on
// `typeof module !== 'undefined'` — a no-op in the browser. Calling loadMore
// in this DOM-free environment is safe: every DOM touch downstream of it
// (render/syncWindowInputs/renderFeed) is itself guarded on the relevant
// element existing (svg/winEls/feedEls.list), all of which stay null here
// since init() never runs without `document`.
'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');

const { loadMore, state } = require(path.join(__dirname, 'timeline.js'));

let failures = 0;
let checks = 0;

function check(description, fn) {
  checks++;
  try {
    fn();
  } catch (err) {
    failures++;
    console.error(`FAIL: ${description}\n  ${err.message}`);
  }
}

function cs(repo, commitSha, committedAt) {
  return { repo, commitSha, author: 'a', committedAt, changes: [] };
}

// installControllableXHR replaces global.XMLHttpRequest with a stub whose
// open()/send() are no-ops and whose onload is never auto-invoked — the
// caller drives resolution manually (by setting .status/.responseText and
// then calling the returned accessor's captured instance's onload()),
// simulating an in-flight, not-yet-resolved fetch for as long as the test
// wants.
function installControllableXHR() {
  let captured = null;
  function FakeXHR() {
    captured = this;
    this.onload = null;
    this.onerror = null;
    this.status = 200;
    this.responseText = '';
  }
  FakeXHR.prototype.open = function () {};
  FakeXHR.prototype.send = function () {};
  global.XMLHttpRequest = FakeXHR;
  return { get instance() { return captured; } };
}

function resetState() {
  state.changesets = [];
  state.loaded = true;
  state.hasFitWindow = true;
  state.nextCursor = '';
  state.loadingMore = false;
  state.loadMoreError = false;
}

// ---- CRITICAL: windowCoversAllData() must be recomputed at use-time ----

check('loadMore recomputes windowCoversAllData() at use-time (inside the fetch callback) rather than capturing it before the async fetch fires — a zoom the user makes WHILE the fetch is in flight must survive the response', () => {
  resetState();
  const early = cs('r1', 'a', '2024-01-01T00:00:00.000Z');
  const late = cs('r1', 'b', '2024-01-02T00:00:00.000Z');
  state.changesets = [early, late];
  state.nextCursor = 'cursor-1';
  // Window covers all currently-loaded data at the moment loadMore() fires.
  state.windowEnd = new Date('2024-01-03T00:00:00.000Z').getTime();
  state.windowMs = state.windowEnd - new Date('2023-12-31T00:00:00.000Z').getTime();

  const xhr = installControllableXHR();
  loadMore();
  assert.ok(xhr.instance, 'loadMore did not issue a request');
  assert.equal(state.loadingMore, true, 'loadMore did not set the in-flight guard');

  // Act: the user zooms into a tight sub-range WHILE the fetch above is
  // still unresolved — nothing disables drag-zoom/wheel-zoom/pan/input-edit
  // during an in-flight "Load more" fetch, so this must be possible and its
  // result must be respected.
  const zoomedWindowEnd = new Date('2024-01-01T01:00:00.000Z').getTime();
  const zoomedWindowMs = 60 * 60 * 1000;
  state.windowEnd = zoomedWindowEnd;
  state.windowMs = zoomedWindowMs;

  // Now let the mocked fetch resolve with the next (older) page.
  xhr.instance.status = 200;
  xhr.instance.responseText = JSON.stringify({
    changesets: [cs('r1', 'c', '2023-12-31T00:00:00.000Z')],
    nextCursor: ''
  });
  xhr.instance.onload();

  assert.equal(state.windowEnd, zoomedWindowEnd, 'loadMore clobbered the window the user zoomed to WHILE the fetch was in flight — the pre-fetch windowCoversAllData() snapshot was stale by the time the response arrived');
  assert.equal(state.windowMs, zoomedWindowMs, 'loadMore clobbered the window the user zoomed to WHILE the fetch was in flight — the pre-fetch windowCoversAllData() snapshot was stale by the time the response arrived');
});

check('loadMore still re-fits the window when it covered all data BOTH when the fetch fired AND when the response arrives (the common, unzoomed case) — the fix must not disable the legitimate refit', () => {
  resetState();
  const early = cs('r1', 'a', '2024-01-01T00:00:00.000Z');
  const late = cs('r1', 'b', '2024-01-02T00:00:00.000Z');
  state.changesets = [early, late];
  state.nextCursor = 'cursor-1';
  state.windowEnd = new Date('2024-01-03T00:00:00.000Z').getTime();
  state.windowMs = state.windowEnd - new Date('2023-12-31T00:00:00.000Z').getTime();
  const windowEndBefore = state.windowEnd;
  const windowMsBefore = state.windowMs;

  const xhr = installControllableXHR();
  loadMore();
  // No mutation this time — window is left exactly as it was, still covering
  // all (soon to be even more) data.
  xhr.instance.status = 200;
  xhr.instance.responseText = JSON.stringify({
    changesets: [cs('r1', 'c', '2023-06-01T00:00:00.000Z')],
    nextCursor: ''
  });
  xhr.instance.onload();

  assert.notEqual(state.windowEnd, windowEndBefore, 'loadMore did not re-fit the window even though it still covered all data when the response arrived');
  assert.notEqual(state.windowMs, windowMsBefore, 'loadMore did not re-fit the window even though it still covered all data when the response arrived');
});

// ---- HIGH: a fetch failure during Load more must be distinguishable from a real end-of-data ----

check('a fetch failure (non-200) during loadMore leaves state.nextCursor untouched instead of collapsing to the server\'s legitimate "no more pages" signal', () => {
  resetState();
  state.changesets = [cs('r1', 'a', '2024-01-01T00:00:00.000Z')];
  state.nextCursor = 'cursor-keep-me';

  const xhr = installControllableXHR();
  loadMore();
  xhr.instance.status = 500;
  xhr.instance.onload();

  assert.equal(state.nextCursor, 'cursor-keep-me', 'a transient fetch failure during Load more must not be indistinguishable from a real empty nextCursor — the Load more control would silently vanish with no error surfaced');
  assert.equal(state.loadingMore, false, 'loadMore must clear the in-flight guard even when the fetch failed, or a retry can never fire');
});

check('a fetch failure during loadMore does not merge/append anything and does not drop already-loaded Changesets', () => {
  resetState();
  const existing = cs('r1', 'a', '2024-01-01T00:00:00.000Z');
  state.changesets = [existing];
  state.nextCursor = 'cursor-keep-me';

  const xhr = installControllableXHR();
  loadMore();
  xhr.instance.status = 0; // network error surfaces as onerror in real XHR; onload with status 0 covers the non-200 branch too
  xhr.instance.onload();

  assert.deepEqual(state.changesets, [existing], 'a failed Load more fetch must never mutate the already-loaded Changesets');
});

check('a genuinely empty nextCursor on a SUCCESSFUL response (real end-of-data) is still honored — the failure-distinguishing fix must not mask the legitimate case', () => {
  resetState();
  state.changesets = [cs('r1', 'a', '2024-01-01T00:00:00.000Z')];
  state.nextCursor = 'cursor-1';

  const xhr = installControllableXHR();
  loadMore();
  xhr.instance.status = 200;
  xhr.instance.responseText = JSON.stringify({ changesets: [cs('r1', 'b', '2024-01-02T00:00:00.000Z')], nextCursor: '' });
  xhr.instance.onload();

  assert.equal(state.nextCursor, '', 'a real end-of-data response (200, empty nextCursor) must still clear state.nextCursor so the Load more row disappears');
  assert.equal(state.changesets.length, 2, 'a successful page must still be merged in');
});

if (failures > 0) {
  console.error(`\n${failures}/${checks} checks failed`);
  process.exitCode = 1;
} else {
  console.log(`PASS: ${checks}/${checks} checks passed (load-more behavior test)`);
}
