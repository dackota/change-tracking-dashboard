# Changelog

## [0.4.3](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.4.2...app-v0.4.3) (2026-07-07)


### Bug Fixes

* reset state.backdropError synchronously in loadBackdrop() ([#64](https://github.com/dackota/change-tracking-dashboard/issues/64)) ([1e0d459](https://github.com/dackota/change-tracking-dashboard/commit/1e0d45919019229a45d59d5ba2b20389058fab7e))

## [0.4.2](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.4.1...app-v0.4.2) (2026-07-07)


### Bug Fixes

* exclude reserved query-param names from FacetOptions() ([#58](https://github.com/dackota/change-tracking-dashboard/issues/58)) ([5543396](https://github.com/dackota/change-tracking-dashboard/commit/5543396b5f063bfaac5d22f42593829c2ea89a28))
* extend poll-chip wrap override to the 860px sidebar-collapse breakpoint ([#57](https://github.com/dackota/change-tracking-dashboard/issues/57)) ([88a7099](https://github.com/dackota/change-tracking-dashboard/commit/88a70995dd2f09c7b00870b29a7749cccc76b433))
* surface backdrop fetch failures instead of the empty state ([#60](https://github.com/dackota/change-tracking-dashboard/issues/60)) ([a548c6f](https://github.com/dackota/change-tracking-dashboard/commit/a548c6f700b956e6204b34db8656f18b2d42beb5))

## [0.4.1](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.4.0...app-v0.4.1) (2026-07-07)


### Bug Fixes

* update stale fetchBackdrop test reference to fetchChangesetsPage ([#59](https://github.com/dackota/change-tracking-dashboard/issues/59)) ([91cf14d](https://github.com/dackota/change-tracking-dashboard/commit/91cf14d841f837b3299bad0dbf4aac9d33885e89))

## [0.4.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.3.0...app-v0.4.0) (2026-07-07)


### Features

* feed pagination ([#55](https://github.com/dackota/change-tracking-dashboard/issues/55)) ([62072f2](https://github.com/dackota/change-tracking-dashboard/commit/62072f2f4d0886b41355140b517dafeb3c1b1db5))
* repository filter scoping the changeset feed ([#54](https://github.com/dackota/change-tracking-dashboard/issues/54)) ([d38f655](https://github.com/dackota/change-tracking-dashboard/commit/d38f65572b26c29798f762903e9109c8e65c5b50))
* responsive layout breakpoints across the shared shell ([#53](https://github.com/dackota/change-tracking-dashboard/issues/53)) ([0b0598d](https://github.com/dackota/change-tracking-dashboard/commit/0b0598d1a601b43606184961f539c368ae7a6b4b))

## [0.3.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.2.0...app-v0.3.0) (2026-07-07)


### Features

* v0.3 — Changes/Repositories views + poll-health surface ([#51](https://github.com/dackota/change-tracking-dashboard/issues/51)) ([f2985ff](https://github.com/dackota/change-tracking-dashboard/commit/f2985ffe9bac5692751aa3097147cf6fc7e86f36))

## [0.2.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.1.5...app-v0.2.0) (2026-07-07)


### Features

* v0.2 — shell completion, poll-status operability & UI polish ([#49](https://github.com/dackota/change-tracking-dashboard/issues/49)) ([74c5bd3](https://github.com/dackota/change-tracking-dashboard/commit/74c5bd3baff9824b98b71ac5973ac06ec6734231))

## [0.1.5](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.1.4...app-v0.1.5) (2026-07-06)


### Bug Fixes

* use Generic updater for appVersion, add merge-safe separator ([#45](https://github.com/dackota/change-tracking-dashboard/issues/45)) ([25d9636](https://github.com/dackota/change-tracking-dashboard/commit/25d963651f5809369ec6941d6e45e58e42614b19))

## [0.1.4](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.1.3...app-v0.1.4) (2026-07-06)


### Bug Fixes

* target appVersion + skip go-verify on non-Go PRs ([#42](https://github.com/dackota/change-tracking-dashboard/issues/42)) ([78a1d0a](https://github.com/dackota/change-tracking-dashboard/commit/78a1d0a6c7db5c0fe05d27560913abcdc799a537))

## [0.1.3](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.1.2...app-v0.1.3) (2026-07-06)


### Bug Fixes

* close .git/trailing-slash class in repoShortName (Go + JS) ([#36](https://github.com/dackota/change-tracking-dashboard/issues/36)) ([ed4bc8f](https://github.com/dackota/change-tracking-dashboard/commit/ed4bc8f76df5b18a6036f470396aa1c5b9ea4e39))
* pin gh CLI --repo + restore App issue labels for release-please auto-merge ([#39](https://github.com/dackota/change-tracking-dashboard/issues/39)) ([0498513](https://github.com/dackota/change-tracking-dashboard/commit/0498513a8d9da2ff8d59a01e8df98ae13d1a462c))
* poll REST check-runs instead of gh pr checks (Actions permission gap) ([#41](https://github.com/dackota/change-tracking-dashboard/issues/41)) ([7f96a70](https://github.com/dackota/change-tracking-dashboard/commit/7f96a70274f0e256307fc4c9d3eb427429fa4fdc))
* report only missing env vars in GitHub App partial-config error ([#40](https://github.com/dackota/change-tracking-dashboard/issues/40)) ([659d17f](https://github.com/dackota/change-tracking-dashboard/commit/659d17fc4c7038c3771a25af066b2d279e9de617))

## [0.1.2](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.1.1...app-v0.1.2) (2026-07-06)


### Bug Fixes

* close .git/trailing-slash class in repoShortName (Go + JS) ([#36](https://github.com/dackota/change-tracking-dashboard/issues/36)) ([ed4bc8f](https://github.com/dackota/change-tracking-dashboard/commit/ed4bc8f76df5b18a6036f470396aa1c5b9ea4e39))
