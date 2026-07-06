// timeline.regression.js — the R11-R15 preserved-interaction regression
// harness for the visual-system-regression slice. It drives the real,
// booted dashboard (see ../regression_harness_test.go, which seeds a
// realistic dataset and starts an httptest.Server wired with the exact
// production HTTP handlers) via a headless **system** Chrome/Chromium
// through playwright-core — the same mechanism used to validate PR #25 and
// the prior slices' runtime gates.
//
// This is a plain Node script, like commit-link.property.test.js: no test
// framework, and no committed package.json/lock — the repo has neither, and
// this slice keeps it that way (see this file's own footprint note below).
// It is NOT part of `go test ./...` or CI (see regression_harness_test.go's
// `regression` build tag); it is a local/validation-time regression proof
// invoked exactly as that file documents:
//
//   npm install --no-save playwright-core   # one-time; writes only a
//                                            # gitignored node_modules/ next
//                                            # to this file — never a
//                                            # package.json/lock
//   go test -race -tags regression -run TestUIRegression -v ./internal/web/...
//
// It expects three environment variables, set by regression_harness_test.go:
//   BASE_URL        the booted dashboard's origin (e.g. http://127.0.0.1:PORT)
//   CHROME_PATH     a system Chrome/Chromium executable (never downloaded)
//   CHART_BUMP_SHA  the full commit sha of the seeded chart-bump Changeset
//
// Element lookups by visible text go through page.evaluateHandle() + a
// plain textContent comparison, then a real ElementHandle.click() — CSS
// text pseudo-classes (:text-is/:has-text/:has(...:text-is)) proved
// unreliable to compose across this playwright-core build's selector
// engine, so this harness avoids them entirely in favor of the one
// mechanism verified to work consistently. Every assertion below targets
// *observable behavior* (DOM state, element counts, rendered text) through
// the same interactions a person performs — clicks, drags, wheel, keyboard
// modifiers — never internal JS state.
'use strict';

const assert = require('node:assert/strict');
const { chromium } = require('playwright-core');

const BASE_URL = process.env.BASE_URL;
const CHROME_PATH = process.env.CHROME_PATH;
const CHART_BUMP_SHA = process.env.CHART_BUMP_SHA;

if (!BASE_URL || !CHROME_PATH || !CHART_BUMP_SHA) {
  console.error('missing required env vars: BASE_URL, CHROME_PATH, CHART_BUMP_SHA must all be set');
  process.exit(1);
}

const NAV_TIMEOUT_MS = 15000;
const ACTION_TIMEOUT_MS = 8000;
const FACET_RELOAD_TIMEOUT_MS = 8000;

let checks = 0;
let failures = 0;

async function check(description, fn) {
  checks++;
  try {
    await fn();
    console.log(`ok - ${description}`);
  } catch (err) {
    failures++;
    console.error(`FAIL: ${description}\n  ${err && err.stack ? err.stack : err}`);
  }
}

// ---- element lookup by visible text (see file-header note on why) ----
//
// Every lookup below is dispatched via page.evaluateHandle (never
// root.evaluateHandle) because Playwright calls an ElementHandle's own
// evaluateHandle with a different argument shape ((element, arg) => ...)
// than Page's ((arg) => ...) — always going through page keeps one
// consistent (arg) => ... signature. root (an ElementHandle or null for the
// whole document) travels inside the arg object, where Playwright
// transparently resolves it back to the live DOM node in-page.

// elementByText finds the first element matching selector (scoped to root,
// or the whole document when root is null) whose trimmed textContent
// exactly equals text, and returns it as a real ElementHandle (so callers
// get Playwright's normal actionability-checked .click()). Throws with a
// clear message if none matched.
async function elementByText(page, root, selector, text) {
  const handle = await page.evaluateHandle(
    ({ root, selector, text }) => {
      const scope = root || document;
      const candidates = Array.from(scope.querySelectorAll(selector));
      return candidates.find((el) => el.textContent.trim() === text) || null;
    },
    { root, selector, text }
  );
  const el = handle.asElement();
  if (!el) {
    throw new Error(`no element matching ${JSON.stringify(selector)} with exact text ${JSON.stringify(text)}`);
  }
  return el;
}

// elementByChildText is like elementByText, but text is matched against any
// one of possibly several descendants (childSelector) while the returned
// handle is the ancestor (selector) — e.g. "the <tr> containing a <td>
// (any of its several <td> cells) with this exact text".
async function elementByChildText(page, root, selector, childSelector, text) {
  const handle = await page.evaluateHandle(
    ({ root, selector, childSelector, text }) => {
      const scope = root || document;
      const candidates = Array.from(scope.querySelectorAll(selector));
      return candidates.find((el) =>
        Array.from(el.querySelectorAll(childSelector)).some((child) => child.textContent.trim() === text)
      ) || null;
    },
    { root, selector, childSelector, text }
  );
  const el = handle.asElement();
  if (!el) {
    throw new Error(`no element matching ${JSON.stringify(selector)} with a ${JSON.stringify(childSelector)} descendant of exact text ${JSON.stringify(text)}`);
  }
  return el;
}

// onlyButtonNextTo returns the ElementHandle of the ".facet-only" button
// that is a sibling of pillHandle within the same .facet-row.
async function onlyButtonNextTo(page, pillHandle) {
  const handle = await page.evaluateHandle(
    ({ pill }) => pill.parentElement.querySelector('.facet-only'),
    { pill: pillHandle }
  );
  const el = handle.asElement();
  if (!el) {
    throw new Error('no .facet-only button found next to the given pill');
  }
  return el;
}

// firstClusterBubble returns the first <g> under selector that has a <text>
// child (a cluster count bubble — see timeline.js's render()), scoped to
// root (or the whole document when root is null), or null if none exists.
async function firstClusterBubble(page, root, selector) {
  const handle = await page.evaluateHandle(
    ({ root, selector }) => {
      const scope = root || document;
      return Array.from(scope.querySelectorAll(selector)).find((g) => g.querySelector('text')) || null;
    },
    { root, selector }
  );
  return handle.asElement();
}

// ---- small DOM/interaction helpers over the real served page ----

async function trackBoundingBox(page) {
  return page.$eval('.timeline-svg', (el) => {
    const r = el.getBoundingClientRect();
    return { x: r.x, y: r.y, width: r.width, height: r.height };
  });
}

async function windowInputValues(page) {
  const [from, to] = await page.$$eval('.range-label input', (inputs) => inputs.map((i) => i.value));
  return { from, to };
}

function minutesBetween(fromLocal, toLocal) {
  return (Date.parse(toLocal) - Date.parse(fromLocal)) / 60000;
}

async function feedRowCount(page) {
  return page.$$eval('#feed-list tr.feed-row', (rows) => rows.length);
}

async function waitForBackdropReload(page, action) {
  const [response] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/changesets?') && r.request().method() === 'GET', {
      timeout: FACET_RELOAD_TIMEOUT_MS,
    }),
    action(),
  ]);
  assert.equal(response.status(), 200, 'facet-triggered backdrop reload must succeed');
}

// ---- R12: dated day/time axis ----

async function testDatedAxis(page) {
  await check('the timeline renders a dated day/time axis (baseline + tick marks + tick labels)', async () => {
    const tickLineCount = await page.$$eval('.timeline-svg line', (lines) => lines.length);
    // 1 baseline + AXIS_TICKS(6)+1 tick marks = 8 <line> elements minimum.
    assert.ok(tickLineCount >= 8, `expected at least 8 axis <line> elements, got ${tickLineCount}`);

    const tickLabels = await page.$$eval('.timeline-svg > text', (texts) =>
      texts.map((t) => t.textContent).filter((t) => t && t.trim().length > 0)
    );
    assert.ok(tickLabels.length >= 6, `expected at least 6 dated tick labels, got ${JSON.stringify(tickLabels)}`);
  });
}

// ---- R11: facet dropdowns, cycling, "only", badges, Clear filters ----

async function testFacetFiltering(page) {
  const componentDD = await elementByChildText(page, null, 'details.facet-dd', '.facet-dd-name', 'component');

  await check('the component facet dropdown opens and exposes both seeded values', async () => {
    const summary = await componentDD.$('summary');
    await summary.click();
    await page.waitForFunction((dd) => dd.open === true, componentDD, { timeout: ACTION_TIMEOUT_MS });
    const values = await componentDD.$$eval('.facet-pill', (pills) => pills.map((p) => p.textContent.trim()).sort());
    assert.deepEqual(values, ['api', 'web'], `expected component values [api, web], got ${JSON.stringify(values)}`);
  });

  const webPill = await elementByText(page, componentDD, '.facet-pill', 'web');
  await check('a facet pill cycles off -> include -> exclude -> off on repeated clicks', async () => {
    await waitForBackdropReload(page, () => webPill.click());
    assert.equal(await webPill.getAttribute('data-state'), 'include');

    await waitForBackdropReload(page, () => webPill.click());
    assert.equal(await webPill.getAttribute('data-state'), 'exclude');

    await waitForBackdropReload(page, () => webPill.click());
    assert.equal(await webPill.getAttribute('data-state'), 'off');
  });

  const apiPill = await elementByText(page, componentDD, '.facet-pill', 'api');
  await check('the "only" shortcut includes one value and excludes every sibling value in the same facet', async () => {
    const apiOnlyEl = await onlyButtonNextTo(page, apiPill);
    await waitForBackdropReload(page, () => apiOnlyEl.click());
    assert.equal(await apiPill.getAttribute('data-state'), 'include');
    assert.equal(await webPill.getAttribute('data-state'), 'exclude');
  });

  await check('the facet badge reflects the active count and Clear filters becomes visible', async () => {
    const badgeText = await componentDD.$eval('.facet-dd-badge', (el) => el.textContent);
    assert.equal(badgeText, '2', 'both the included and excluded value should count toward the badge');
    const clearHidden = await page.getAttribute('#facet-clear', 'hidden');
    assert.equal(clearHidden, null, 'Clear filters must be visible while a filter is active');
  });

  await check('Clear filters resets every facet to off in one click', async () => {
    await waitForBackdropReload(page, () => page.click('#facet-clear'));
    assert.equal(await webPill.getAttribute('data-state'), 'off');
    assert.equal(await apiPill.getAttribute('data-state'), 'off');
    const clearHiddenAfter = await page.getAttribute('#facet-clear', 'hidden');
    assert.notEqual(clearHiddenAfter, null, 'Clear filters must hide itself once no filter is active');
  });
}

// ---- R12/R13: drag-to-zoom, scroll-to-zoom, shift-drag pan, Reset zoom ----

async function testZoomPanReset(page) {
  const totalRows = await feedRowCount(page);

  await check('drag-to-zoom narrows the visible window and the feed follows it', async () => {
    const before = await windowInputValues(page);
    const box = await trackBoundingBox(page);
    const cy = box.y + box.height / 2;

    // Drag over the rightmost slice of the track — the recent cluster +
    // chart-bump Changesets live there; the two 20/30-day-old ones do not.
    await page.mouse.move(box.x + box.width * 0.82, cy);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width * 0.97, cy, { steps: 8 });
    await page.mouse.up();

    const after = await windowInputValues(page);
    assert.notEqual(after.from, before.from, 'From input did not change after drag-to-zoom');
    assert.notEqual(after.to, before.to, 'To input did not change after drag-to-zoom');

    const rowsAfter = await feedRowCount(page);
    assert.ok(rowsAfter < totalRows, `expected fewer than ${totalRows} feed rows after zooming into a sub-range, got ${rowsAfter}`);
  });

  await check('scroll-to-zoom (wheel) narrows the window further', async () => {
    const beforeVals = await windowInputValues(page);
    const spanBefore = minutesBetween(beforeVals.from, beforeVals.to);
    const box = await trackBoundingBox(page);
    await page.mouse.move(box.x + box.width * 0.9, box.y + box.height / 2);
    await page.mouse.wheel(0, -300); // negative deltaY == zoom in
    const afterVals = await windowInputValues(page);
    const spanAfter = minutesBetween(afterVals.from, afterVals.to);
    assert.ok(spanAfter < spanBefore, `expected window span to shrink after scroll-zoom-in (before=${spanBefore}min, after=${spanAfter}min)`);
  });

  await check('shift-drag pans the window (span stays constant, start/end both shift)', async () => {
    const beforePan = await windowInputValues(page);
    const spanBeforePan = minutesBetween(beforePan.from, beforePan.to);

    const box = await trackBoundingBox(page);
    const cy = box.y + box.height / 2;
    await page.mouse.move(box.x + box.width * 0.3, cy);
    await page.keyboard.down('Shift');
    await page.mouse.down();
    await page.mouse.move(box.x + box.width * 0.6, cy, { steps: 8 });
    await page.mouse.up();
    await page.keyboard.up('Shift');

    const afterPan = await windowInputValues(page);
    const spanAfterPan = minutesBetween(afterPan.from, afterPan.to);
    assert.notEqual(afterPan.from, beforePan.from, 'From input did not change after a shift-drag pan');
    assert.ok(Math.abs(spanAfterPan - spanBeforePan) <= 1, `pan must preserve the window span (before=${spanBeforePan}min, after=${spanAfterPan}min)`);
  });

  await check("Reset zoom (the header's global action) returns the window to cover the full data span", async () => {
    await page.click('#header-reset-zoom');
    const rowsAfterReset = await feedRowCount(page);
    assert.equal(rowsAfterReset, totalRows, `expected all ${totalRows} feed rows after Reset zoom, got ${rowsAfterReset}`);
    const embeddedResetDisabled = await page.getAttribute('.range-clear', 'disabled');
    assert.notEqual(embeddedResetDisabled, null, 'the embedded Reset zoom control should be disabled once the window covers all data');
  });
}

// ---- R14: cluster click expands; flags re-cluster on render ----

async function clusterBubbleCount(page) {
  return page.$$eval('.timeline-svg > g', (groups) => groups.filter((g) => g.querySelector('text')).length);
}

async function testClusterExpandAndRecluster(page) {
  await check('a clustered marker (2 near-simultaneous Changesets) renders as one count bubble at full zoom', async () => {
    const count = await clusterBubbleCount(page);
    assert.equal(count, 1, `expected exactly 1 cluster bubble at the full-zoom view, got ${count}`);
  });

  await check('clicking the cluster marker zooms in so its members split into separate markers', async () => {
    const clusterHandle = await firstClusterBubble(page, null, '.timeline-svg > g');
    assert.ok(clusterHandle, 'no cluster bubble <g> found to click');
    await clusterHandle.click();

    await page.waitForFunction(
      () => document.querySelectorAll('.timeline-svg circle[data-commit-sha]').length === 2,
      { timeout: ACTION_TIMEOUT_MS }
    );
    const remainingBubbles = await clusterBubbleCount(page);
    assert.equal(remainingBubbles, 0, 'the cluster must fully split apart (re-cluster on render) once zoomed to its own span');
  });
}

// ---- R15: Changeset detail (marker + feed row) + hunked Chart diff ----

async function testChangesetDetailAndChartDiff(page) {
  // Return to the full view so every seeded Changeset (marker and feed row)
  // is present again before exercising detail-open from each entry point.
  await page.click('#header-reset-zoom');

  await check('clicking a timeline marker opens the Changeset detail panel', async () => {
    const markerHandle = await page.$('.timeline-svg circle[data-commit-sha]');
    assert.ok(markerHandle, 'no individual marker found to click');
    await markerHandle.click();
    await page.waitForSelector('#timeline-detail-panel .changeset-detail', { timeout: ACTION_TIMEOUT_MS });
  });

  const shortSha = CHART_BUMP_SHA.slice(0, 8);
  await check("clicking the chart-bump Changeset's feed row opens its detail with a chart-kind Change", async () => {
    const rowHandle = await elementByChildText(page, null, '#feed-list tr.feed-row', 'td', shortSha);
    await rowHandle.click();

    await page.waitForSelector(`#timeline-detail-panel .changeset-detail[data-commit-sha="${CHART_BUMP_SHA}"] .change-kind-chart`, {
      timeout: ACTION_TIMEOUT_MS,
    });
  });

  await check('the chart-kind Change renders the collapsed, color-coded (red/green) hunked Chart diff', async () => {
    const slotSelector = `#timeline-detail-panel .changeset-detail[data-commit-sha="${CHART_BUMP_SHA}"] .change-helm-diff-slot`;
    await page.waitForSelector(`${slotSelector} .diff-hunks`, { timeout: ACTION_TIMEOUT_MS });

    const addedLines = await page.$$eval(`${slotSelector} .diff-add`, (els) => els.map((e) => e.textContent));
    const removedLines = await page.$$eval(`${slotSelector} .diff-del`, (els) => els.map((e) => e.textContent));

    assert.ok(
      addedLines.some((l) => l.includes('+') && l.includes('replicas: 2')),
      `expected an added (+) hunk line mentioning "replicas: 2"; got ${JSON.stringify(addedLines)}`
    );
    assert.ok(
      removedLines.some((l) => l.includes('-') && l.includes('replicas: 1')),
      `expected a removed (-) hunk line mentioning "replicas: 1"; got ${JSON.stringify(removedLines)}`
    );
  });
}

// ---- driver ----

async function main() {
  const browser = await chromium.launch({ executablePath: CHROME_PATH, headless: true });
  const consoleErrors = [];
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(ACTION_TIMEOUT_MS);
    page.on('console', (msg) => {
      if (msg.type() === 'error') { consoleErrors.push(msg.text()); }
    });
    page.on('pageerror', (err) => { consoleErrors.push(String(err)); });

    await page.goto(BASE_URL, { waitUntil: 'networkidle', timeout: NAV_TIMEOUT_MS });
    await page.waitForFunction(() => document.querySelectorAll('#feed-list tr.feed-row').length > 0, { timeout: NAV_TIMEOUT_MS });

    await testDatedAxis(page);
    await testFacetFiltering(page);
    await testZoomPanReset(page);
    await testClusterExpandAndRecluster(page);
    await testChangesetDetailAndChartDiff(page);

    await check('no unexpected browser console errors were observed during the run', () => {
      assert.deepEqual(consoleErrors, [], `console errors: ${JSON.stringify(consoleErrors)}`);
    });
  } finally {
    await browser.close();
  }
}

main()
  .then(() => {
    if (failures > 0) {
      console.error(`\n${failures}/${checks} checks failed`);
      process.exitCode = 1;
    } else {
      console.log(`PASS: ${checks}/${checks} checks passed (UI regression harness)`);
    }
  })
  .catch((err) => {
    console.error('UI regression harness crashed:', err && err.stack ? err.stack : err);
    process.exitCode = 1;
  });
