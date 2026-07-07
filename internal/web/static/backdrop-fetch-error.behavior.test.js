// backdrop-fetch-error.behavior.test.js — regression coverage for a
// reachable HIGH+MEDIUM finding from the feed-pagination slice's correctness
// gate: renderBackdrop/loadBackdrop (the fetchChangesetsPage onDone for a
// FRESH first page — fired by init() on initial load, and by
// onFilterChanged() on every facet/repo-scope change) did not distinguish a
// genuine fetch failure (non-200/malformed JSON/network error) from a real
// successful empty page, the same distinction fetchChangesetsPage's own doc
// comment describes and loadMore() already honors for the "Load more"
// affordance. A backdrop failure rendered the misleading "No changes
// recorded yet — the poller may still be backfilling." empty state instead
// of an honest error, AND (on a filter-reload failure) clobbered
// state.changesets/state.nextCursor with the failure's empty payload,
// silently discarding whatever was already showing.
//
// fetchChangesetsPage (and therefore loadBackdrop) issues its request via
// XMLHttpRequest, not the fetch() API — this file stubs a controllable,
// manually-resolved global.XMLHttpRequest (open/send are no-ops; the test
// invokes the captured instance's onload itself, whenever it chooses to)
// rather than global.fetch, mirroring load-more.behavior.test.js's own stub.
//
// Like load-more.behavior.test.js, this suite asserts at the `state` level
// (state.backdropError, state.changesets, state.nextCursor, state.loaded) —
// not against actual rendered DOM — because every DOM touch downstream of
// loadBackdrop (render/syncWindowInputs/renderFeed) is itself guarded on the
// relevant element existing (svg/winEls/feedEls.list), all of which stay
// null in this DOM-free Node environment. state.backdropError IS the
// "surfaced error state" the acceptance criteria asks to prove: renderFeed
// branches on it to render buildBackdropErrorRow() instead of the "No
// changes recorded yet" empty row, exactly mirroring how state.loadMoreError
// (asserted the same way in load-more.behavior.test.js) drives
// buildLoadMoreRow()'s inline error message.
//
// This is a plain Node script (no test framework, no new npm dependency),
// mirroring this directory's other *.test.js files' conventions. Run with:
//
//   node internal/web/static/backdrop-fetch-error.behavior.test.js
'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');

const { loadBackdrop, state } = require(path.join(__dirname, 'timeline.js'));

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
// caller drives resolution manually, mirroring load-more.behavior.test.js's
// own stub exactly (fetchChangesetsPage is shared by loadMore and
// loadBackdrop alike).
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
  state.loaded = false;
  state.hasFitWindow = false;
  state.nextCursor = '';
  state.loadingMore = false;
  state.loadMoreError = false;
  state.backdropError = false;
}

// ---- (a) a backdrop fetch failure surfaces the error state, not the empty state ----

check('a backdrop fetch failure on a fresh initial load (no prior data) sets state.backdropError, clears the loading state, and leaves state.changesets/nextCursor at their empty starting values', () => {
  resetState();

  const xhr = installControllableXHR();
  loadBackdrop();
  assert.ok(xhr.instance, 'loadBackdrop did not issue a request');

  xhr.instance.status = 500;
  xhr.instance.onload();

  assert.equal(state.backdropError, true, 'a fetch failure must set state.backdropError so renderFeed can render an honest error instead of "No changes recorded yet"');
  assert.equal(state.loaded, true, 'a failed fetch must still clear the loading state, or the feed is stuck showing "Loading changes…" forever');
  assert.deepEqual(state.changesets, [], 'a fresh load with no prior data has nothing to preserve — changesets stay empty');
  assert.equal(state.nextCursor, '', 'a fresh load with no prior data has nothing to preserve — nextCursor stays empty');
});

check('a malformed-JSON backdrop response (200 status, unparsable body) is treated as a failure too — same as the non-200 path', () => {
  resetState();

  const xhr = installControllableXHR();
  loadBackdrop();
  xhr.instance.status = 200;
  xhr.instance.responseText = '{not valid json';
  xhr.instance.onload();

  assert.equal(state.backdropError, true, 'malformed JSON must surface the same error state as a non-200 response');
});

// ---- (b) a backdrop failure must never clobber previously-loaded data ----

check('a backdrop fetch failure during a filter-triggered reload sets state.backdropError but does not clobber the already-loaded state.changesets/state.nextCursor', () => {
  resetState();
  const existing = [cs('r1', 'a', '2024-01-01T00:00:00.000Z')];
  state.changesets = existing;
  state.nextCursor = 'cursor-keep-me';
  state.loaded = true;
  state.hasFitWindow = true;

  const xhr = installControllableXHR();
  loadBackdrop();
  xhr.instance.status = 0; // network error surfaces as onload with status 0 too (covers the non-200 branch), mirroring load-more.behavior.test.js's own convention
  xhr.instance.onload();

  assert.equal(state.backdropError, true, 'a filter-reload fetch failure must still surface the error state');
  assert.deepEqual(state.changesets, existing, 'a failed filter-reload fetch must never clobber Changesets that were already showing');
  assert.equal(state.nextCursor, 'cursor-keep-me', 'a failed filter-reload fetch must never clobber the pagination cursor that was already in place');
});

// ---- a subsequent successful reload clears the error state and renders normally ----

check('a subsequent successful reload after a failure clears state.backdropError and replaces state.changesets/state.nextCursor with the fresh page, exactly like any other successful backdrop load', () => {
  resetState();
  state.changesets = [cs('r1', 'a', '2024-01-01T00:00:00.000Z')];
  state.nextCursor = 'stale-cursor';
  state.backdropError = true; // simulate a prior failed load
  state.loaded = true;
  state.hasFitWindow = true;

  const xhr = installControllableXHR();
  loadBackdrop();
  xhr.instance.status = 200;
  xhr.instance.responseText = JSON.stringify({
    changesets: [cs('r1', 'b', '2024-01-02T00:00:00.000Z')],
    nextCursor: 'fresh-cursor'
  });
  xhr.instance.onload();

  assert.equal(state.backdropError, false, 'a successful reload must clear state.backdropError');
  assert.deepEqual(state.changesets, [cs('r1', 'b', '2024-01-02T00:00:00.000Z')], 'a successful reload must replace state.changesets with the fresh page (a fresh load always replaces, never merges)');
  assert.equal(state.nextCursor, 'fresh-cursor', 'a successful reload must replace state.nextCursor with the fresh page\'s cursor');
});

// ---- loadBackdrop must reset its OWN error flag synchronously too, mirroring loadMore ----

check('loadBackdrop() resets a stale state.backdropError synchronously — before its fetch resolves — exactly like loadMore() resets state.loadMoreError, so a prior failure\'s flag never lingers into a fresh reload\'s in-flight window', () => {
  resetState();
  state.backdropError = true; // simulate a prior failed load whose error flag was never cleared

  const xhr = installControllableXHR();
  loadBackdrop();

  assert.ok(xhr.instance, 'loadBackdrop did not issue a request');
  assert.equal(state.backdropError, false, 'loadBackdrop() must reset state.backdropError synchronously when invoked (before the async fetch resolves), the same way it already resets state.loadMoreError — otherwise a stale backdropError=true from a prior failed load persists into the new in-flight window, masked today only by renderFeed\'s !state.loaded early-return and renderBackdrop\'s unconditional overwrite on completion');
});

if (failures > 0) {
  console.error(`\n${failures}/${checks} checks failed`);
  process.exitCode = 1;
} else {
  console.log(`PASS: ${checks}/${checks} checks passed (backdrop fetch error behavior test)`);
}
