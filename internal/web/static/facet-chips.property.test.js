// facet-chips.property.test.js — property/invariant test for timeline.js's
// facetChips(facetState) mapping (R21/R24): the pure transformation from the
// tri-state facetState[facet][value] = 'include' | 'exclude' map into the
// chip model ({facet, value, mode}) that drives the filter-bar's removable
// chips (R21), and that "clear all filters" (R22) resets to empty.
// facetChips must never mutate its input, must emit exactly one chip per
// active (include/exclude) facet/value pair, must never emit a chip for an
// 'off' (or otherwise non-include/exclude) entry, and must return chips in a
// deterministic (facet, then value) order regardless of insertion order or
// malformed nested state.
//
// This is a plain Node script (no test framework, no new npm dependency),
// mirroring internal/web/static/commit-link.property.test.js's own
// conventions (seeded PRNG sweep, example table, adversarial-input sweep).
// Run with:
//
//   node internal/web/static/facet-chips.property.test.js
//
// facetChips is exposed for this file only via the same CommonJS export hook
// at the bottom of timeline.js's IIFE used by commitURL/repoShortName,
// gated on `typeof module !== 'undefined'` — a no-op in the browser.
'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');

const { facetChips } = require(path.join(__dirname, 'timeline.js'));

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

function deepClone(v) { return JSON.parse(JSON.stringify(v)); }

// ---- example table ----

check('empty facetState ({}) yields no chips', () => {
  assert.deepEqual(facetChips({}), []);
});

check('a single include pair yields exactly one include chip', () => {
  assert.deepEqual(facetChips({ env: { prod: 'include' } }), [{ facet: 'env', value: 'prod', mode: 'include' }]);
});

check('a single exclude pair yields exactly one exclude chip', () => {
  assert.deepEqual(facetChips({ env: { staging: 'exclude' } }), [{ facet: 'env', value: 'staging', mode: 'exclude' }]);
});

check('mixed include and exclude across multiple facets yields one chip per pair, sorted by facet then value', () => {
  const chips = facetChips({
    region: { us: 'exclude', eu: 'include' },
    env: { prod: 'include', dev: 'exclude' }
  });
  assert.deepEqual(chips, [
    { facet: 'env', value: 'dev', mode: 'exclude' },
    { facet: 'env', value: 'prod', mode: 'include' },
    { facet: 'region', value: 'eu', mode: 'include' },
    { facet: 'region', value: 'us', mode: 'exclude' }
  ]);
});

check('an all-excluded facetState yields one exclude chip per value, none include', () => {
  const chips = facetChips({ tier: { sbx: 'exclude', dev: 'exclude', prod: 'exclude' } });
  assert.equal(chips.length, 3);
  assert.ok(chips.every((c) => c.mode === 'exclude'), 'every chip must be mode exclude');
});

check('an explicit "off" entry never produces a chip (defensive: the real state machine deletes these on cycle-to-off, but facetChips must not trust that)', () => {
  assert.deepEqual(facetChips({ env: { prod: 'off', dev: 'include' } }), [{ facet: 'env', value: 'dev', mode: 'include' }]);
});

check('"clear all filters" (an empty facetState, post-reset) yields no chips', () => {
  // clearAllFilters / clearFacets reset facetState to {} — this is the chip
  // side of R22's contract: the chip model must go empty in lockstep.
  assert.deepEqual(facetChips({}), []);
});

// ---- property/invariant sweep: generated + adversarial inputs ----
//
// A tiny seeded PRNG (not Math.random) keeps the sweep deterministic across
// runs, while still exercising many facetState shapes per invocation.

function makeRng(seed) {
  let state = seed >>> 0;
  return function next() {
    // xorshift32
    state ^= state << 13; state >>>= 0;
    state ^= state >>> 17;
    state ^= state << 5; state >>>= 0;
    return state / 0xffffffff;
  };
}

const rng = makeRng(0xFACE7);

function pick(rng, arr) { return arr[Math.floor(rng() * arr.length)]; }

// FACETS/VALUES intentionally include an entry with internal spaces and an
// empty string — facetChips must treat these like any other key, not throw
// or special-case them.
const FACETS = ['env', 'tenant', 'region', 'a facet with spaces', ''];
const VALUES = ['prod', 'dev', 'sbx', 'a value with spaces', ''];
// 'off' is included here even though the real state machine (setFacetState)
// deletes an entry rather than storing 'off' — facetChips is the defensive
// chokepoint that must still ignore it if it ever appears.
const MODES = ['include', 'exclude', 'off'];

// buildRandomFacetState returns a randomly generated { facetState, expected }
// pair: facetState is the (possibly sparse) tri-state map, and expected is
// the list of {facet, value, mode} for exactly the include/exclude entries —
// the chip model facetChips(facetState) must reduce to.
function buildRandomFacetState(rng) {
  const facetState = {};
  const expected = [];
  for (const facet of FACETS) {
    for (const value of VALUES) {
      if (rng() < 0.4) { continue; } // omitted entirely — the common sparse case
      const mode = pick(rng, MODES);
      if (!facetState[facet]) { facetState[facet] = {}; }
      facetState[facet][value] = mode;
      if (mode === 'include' || mode === 'exclude') {
        expected.push({ facet, value, mode });
      }
    }
  }
  return { facetState, expected };
}

// sortPairs sorts {facet, value, mode} triples by facet then value using
// plain string comparison — the same ordering facetChips itself must produce
// (via Object.keys(...).sort()), so this is the independent "expected" order
// the property sweep checks the implementation against.
function sortPairs(pairs) {
  return pairs.slice().sort((a, b) => {
    if (a.facet !== b.facet) { return a.facet < b.facet ? -1 : 1; }
    if (a.value !== b.value) { return a.value < b.value ? -1 : 1; }
    return 0;
  });
}

check('for many generated facetState inputs: chip count equals the active (include/exclude) pair count, chips are exactly those pairs sorted by facet then value, no chip ever carries mode "off", facetChips does not mutate its input, and repeated calls are deterministic', () => {
  for (let i = 0; i < 300; i++) {
    const { facetState, expected } = buildRandomFacetState(rng);
    const snapshotBefore = deepClone(facetState);

    const chips = facetChips(facetState);
    const chipsAgain = facetChips(facetState);

    assert.deepEqual(facetState, snapshotBefore, `facetChips must not mutate its input; facetState=${JSON.stringify(facetState)}`);
    assert.equal(chips.length, expected.length, `chip count must equal the active-pair count; facetState=${JSON.stringify(facetState)}`);
    assert.deepEqual(chips, sortPairs(expected), `chips must be exactly the active pairs, sorted by facet then value; facetState=${JSON.stringify(facetState)}`);
    assert.ok(chips.every((c) => c.mode === 'include' || c.mode === 'exclude'), `no chip may carry mode "off"; got ${JSON.stringify(chips)}`);
    assert.deepEqual(chipsAgain, chips, 'facetChips must be deterministic across repeated calls on the same input');
  }
});

check('facetChips returns a NEW array each call (identity differs), never the same reference — proving it is not handing back an internal, mutable cache', () => {
  const facetState = { env: { prod: 'include' } };
  const a = facetChips(facetState);
  const b = facetChips(facetState);
  assert.notEqual(a, b, 'facetChips must return a fresh array each call');
});

check('a facetState with only "off"/malformed nested entries (no include/exclude anywhere) yields zero chips', () => {
  const chips = facetChips({ env: { prod: 'off', dev: 'off' }, tenant: {} });
  assert.deepEqual(chips, []);
});

// ---- adversarial: malformed/degenerate facetState shapes never throw ----
//
// facetChips is fed whatever facetState happens to hold — it must be immune
// to malformed nesting (null/undefined/non-object values at any level),
// non-plain-object top-level input, and prototype-adjacent keys.

const MALFORMED_FACET_STATES = [
  null,
  undefined,
  {},
  [],
  42,
  'not-an-object',
  { env: null },
  { env: undefined },
  { env: 'not-an-object' },
  { env: 42 },
  { env: [] },
  { env: { prod: null } },
  { env: { prod: undefined } },
  { env: { prod: 42 } },
  { env: { prod: {} } },
  { env: { prod: ['include'] } },
  { '': { '': 'include' } },
  { 'facet with "quotes" & <script>': { 'value with <html>': 'exclude' } },
  { '__proto__': { x: 'include' } },
  { constructor: { x: 'exclude' } }
];

check('facetChips never throws and always returns an array for malformed/adversarial facetState shapes', () => {
  for (const input of MALFORMED_FACET_STATES) {
    let result;
    assert.doesNotThrow(() => { result = facetChips(input); }, `threw for facetState=${JSON.stringify(input)}`);
    assert.ok(Array.isArray(result), `facetChips must always return an array, got ${typeof result} for facetState=${JSON.stringify(input)}`);
  }
});

check('facetChips handles an oversized facetState (200 facets x 10 values) without throwing and returns exactly one chip per active pair', () => {
  const big = {};
  let wantCount = 0;
  for (let f = 0; f < 200; f++) {
    const facet = 'facet-' + f;
    big[facet] = {};
    for (let v = 0; v < 10; v++) {
      big[facet]['value-' + v] = v % 2 === 0 ? 'include' : 'exclude';
      wantCount++;
    }
  }
  let result;
  assert.doesNotThrow(() => { result = facetChips(big); });
  assert.equal(result.length, wantCount);
});

if (failures > 0) {
  console.error(`\n${failures}/${checks} checks failed`);
  process.exitCode = 1;
} else {
  console.log(`PASS: ${checks}/${checks} checks passed (facet-chips property test)`);
}
