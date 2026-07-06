// commit-link.property.test.js — property/invariant test for timeline.js's
// commitURL(repo, sha) derivation (R10): the pure transformation that decides
// whether a feed row's commit cell becomes a link (http(s) repos) or plain
// short-sha text (local-path / non-http(s) repos), and that never lets the
// sha smuggle a scheme into the emitted URL (the R19 security invariant).
//
// This is a plain Node script (no test framework, no new npm dependency) —
// the repo has no JS package.json/toolchain; Node 18+ ships everything this
// file needs (require, assert). Run with:
//
//   node internal/web/static/commit-link.property.test.js
//
// commitURL is exposed for this file only via a CommonJS export hook at the
// bottom of timeline.js's IIFE, gated on `typeof module !== 'undefined'` —
// a no-op in the browser (module is not defined there), so the shipped
// single first-party <script> is unaffected (R18).
'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');

const { commitURL } = require(path.join(__dirname, 'timeline.js'));

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

// ---- example table: the edge inputs called out explicitly in the PRD ----

const EXAMPLES = [
  { repo: 'https://github.com/org/repo', sha: 'abcdef1234567890', wantLink: true },
  { repo: 'http://example.com/org/repo', sha: 'deadbeef', wantLink: true },
  { repo: 'https://github.com/org/repo/', sha: 'abcdef12', wantLink: true }, // trailing slash
  { repo: 'https://github.com/org/repo.git', sha: 'abcdef12', wantLink: true }, // trailing .git
  { repo: 'https://github.com/org/repo/.git', sha: 'abcdef12', wantLink: true }, // trailing slash + .git
  { repo: 'ssh://git@github.com/org/repo.git', sha: 'abcdef12', wantLink: false }, // ssh:// form
  { repo: 'git@github.com:org/repo.git', sha: 'abcdef12', wantLink: false }, // scp-like git@ form
  { repo: '/repos/local-path', sha: 'abcdef12', wantLink: false }, // bare local path
  { repo: 'local/relative/path', sha: 'abcdef12', wantLink: false }, // bare relative path
  { repo: 'https://github.com/org/repo', sha: '', wantLink: false }, // empty sha
  { repo: 'https://github.com/org/repo', sha: 'a', wantLink: true }, // short sha still links
  { repo: '', sha: 'abcdef12', wantLink: false }, // empty repo
];

for (const { repo, sha, wantLink } of EXAMPLES) {
  check(`commitURL(${JSON.stringify(repo)}, ${JSON.stringify(sha)}) wantLink=${wantLink}`, () => {
    const got = commitURL(repo, sha);
    if (wantLink) {
      assert.notEqual(got, '', 'expected a non-empty commit URL');
    } else {
      assert.equal(got, '', 'expected no link (plain short-sha text) for a non-http(s)/empty-sha input');
    }
  });
}

// ---- property/invariant sweep: generated + adversarial inputs ----
//
// A tiny seeded PRNG (not Math.random) keeps the sweep deterministic across
// runs, while still exercising many repo/sha combinations per invocation.

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

const rng = makeRng(0xC0FFEE);

function pick(rng, arr) { return arr[Math.floor(rng() * arr.length)]; }

// HTTP_REPO_TEMPLATES is generated, not hand-picked: every base repo URL
// crossed with every trailing-slash / ".git"-suffix permutation in the class
// this fix must handle (no suffix, slash(es) alone, a bare ".git", and
// ".git" preceded and/or followed by one or more slashes). The originally
// reported regression ("https://github.com/org/repo/.git" rendering a
// double slash before "/commit/") is one member of this class — generating
// the whole class here, rather than hand-listing that one example, is what
// catches the sibling permutations ("repo.git/", "repo//.git//", …) a
// hand-picked table would have missed.
const BASE_REPOS = [
  'https://github.com/org/repo',
  'http://example.com/org/repo',
  'https://gitlab.example.com/group/sub/repo',
];

const TRAILING_SUFFIXES = [
  '', '/', '//',
  '.git', '.git/', '.git//',
  '/.git', '/.git/', '/.git//',
  '//.git', '//.git//',
];

const HTTP_REPO_TEMPLATES = [];
for (const base of BASE_REPOS) {
  for (const suffix of TRAILING_SUFFIXES) {
    HTTP_REPO_TEMPLATES.push(base + suffix);
  }
}
// Kept as a discrete adversarial example (repeated *internal* slashes, not a
// trailing-suffix permutation, so it isn't produced by the loop above).
HTTP_REPO_TEMPLATES.push('https://github.com///org//repo///');

// Non-linkable: not http(s), or http(s) with the wrong case (the underlying
// regex is intentionally case-sensitive on the scheme — "HTTPS://" is not a
// recognized scheme prefix here and must fall back to plain text, not throw
// or half-link).
const NON_HTTP_REPO_TEMPLATES = [
  'ssh://git@github.com/org/repo.git',
  'git@github.com:org/repo.git',
  '/repos/local-path',
  'local/relative/path',
  '',
  'ftp://example.com/org/repo',
  'HTTPS://github.com/org/repo',
  'file:///repos/local-path',
];

// Adversarial sha values: normal, short, empty, HTML metacharacters, a
// javascript: prefix (protocol-confusion attempt), oversized, whitespace,
// slashes, unicode. None of these may make commitURL throw, and none may
// change the *scheme* of a linked result.
const ADVERSARIAL_SHAS = [
  'abcdef1234567890',
  'a',
  '',
  '<script>alert(1)</script>',
  '"><img src=x onerror=alert(1)>',
  'sha-with-"quotes"-&-amp;',
  'javascript:alert(1)',
  'data:text/html,<script>alert(1)</script>',
  'x'.repeat(2000),
  '  leading-and-trailing-space  ',
  'sha/with/slashes',
  'unicode-λ-☃',
  null,
  undefined,
];

check('commitURL never throws for any (repo, sha) combination, including adversarial input', () => {
  const allRepos = HTTP_REPO_TEMPLATES.concat(NON_HTTP_REPO_TEMPLATES);
  for (let i = 0; i < 400; i++) {
    const repo = pick(rng, allRepos);
    const sha = pick(rng, ADVERSARIAL_SHAS);
    let result;
    assert.doesNotThrow(() => { result = commitURL(repo, sha); }, `threw for repo=${JSON.stringify(repo)} sha=${JSON.stringify(sha)}`);
    assert.equal(typeof result, 'string', `commitURL must always return a string, got ${typeof result} for repo=${JSON.stringify(repo)} sha=${JSON.stringify(sha)}`);
  }
});

check('for every http(s) repo + non-empty sha, the result is a well-formed https(s)://.../commit/<sha> URL ending in the verbatim sha', () => {
  for (const repo of HTTP_REPO_TEMPLATES) {
    for (const sha of ['abcdef12', 'a', '<script>x</script>', 'x'.repeat(500)]) {
      const got = commitURL(repo, sha);
      assert.notEqual(got, '', `expected a link for http(s) repo=${JSON.stringify(repo)} sha=${JSON.stringify(sha)}`);

      const scheme = /^https?:\/\//.exec(repo)[0];
      assert.ok(got.startsWith(scheme), `result must start with the repo's own scheme ${scheme}; got ${got}`);

      // The sha must appear verbatim as the trailing path segment — never
      // dropped, escaped, or otherwise mangled.
      assert.ok(got.endsWith(sha), `result must end with the verbatim sha ${JSON.stringify(sha)}; got ${got}`);

      // "/commit/<sha>" must be the exact suffix — no double slash before it
      // (proves trailing repo slashes were normalized away) and no leftover
      // ".git" immediately before it (proves the .git suffix was stripped).
      const commitSuffix = '/commit/' + sha;
      assert.ok(got.endsWith(commitSuffix), `result must end with "${commitSuffix}"; got ${got}`);
      const beforeSuffix = got.slice(0, got.length - commitSuffix.length);
      assert.ok(!beforeSuffix.endsWith('/'), `must not leave a double slash before /commit/; got ${got}`);
      assert.ok(!beforeSuffix.endsWith('.git'), `must strip a trailing .git before appending /commit/; got ${got}`);

      // Protocol-confusion guard: the sha can never change the *parsed*
      // scheme of the emitted URL, even when the sha itself looks like a
      // competing scheme (javascript:, data:) — the scheme always comes
      // from the already-validated repo prefix, appended-to, never
      // replaced.
      assert.ok(!got.startsWith('javascript:'), `result must never start with javascript: ; got ${got}`);
      assert.ok(!got.startsWith('data:'), `result must never start with data: ; got ${got}`);
    }
  }
});

check('for every non-http(s) repo, or an empty sha, the result is exactly "" (plain short-sha text, no link) for any sha', () => {
  for (const repo of NON_HTTP_REPO_TEMPLATES) {
    for (const sha of ADVERSARIAL_SHAS) {
      const got = commitURL(repo, sha);
      assert.equal(got, '', `expected no link for non-http(s) repo=${JSON.stringify(repo)} sha=${JSON.stringify(sha)}; got ${JSON.stringify(got)}`);
    }
  }
  for (const repo of HTTP_REPO_TEMPLATES) {
    const got = commitURL(repo, '');
    assert.equal(got, '', `expected no link for an empty sha regardless of repo; got ${JSON.stringify(got)} for repo=${repo}`);
  }
});

if (failures > 0) {
  console.error(`\n${failures}/${checks} checks failed`);
  process.exitCode = 1;
} else {
  console.log(`PASS: ${checks}/${checks} checks passed (commit-link property test)`);
}
