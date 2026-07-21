# Changelog

## [0.11.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.8...app-v0.11.0) (2026-07-21)


### Features

* **feed:** show commit subject in the change feed and detail header ([#111](https://github.com/dackota/change-tracking-dashboard/issues/111)) ([36d85ff](https://github.com/dackota/change-tracking-dashboard/commit/36d85ff62890929760585c906c7f6db8ce6a792a))

## [0.10.8](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.7...app-v0.10.8) (2026-07-20)


### Bug Fixes

* **poller:** key high-water-mark by field so every field on a file backfills ([#109](https://github.com/dackota/change-tracking-dashboard/issues/109)) ([e8fd33d](https://github.com/dackota/change-tracking-dashboard/commit/e8fd33d539de8d10424795a4ce604a5035600188)), closes [#108](https://github.com/dackota/change-tracking-dashboard/issues/108)

## [0.10.7](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.6...app-v0.10.7) (2026-07-20)


### Bug Fixes

* **deps:** update module helm.sh/helm/v3 to v4 ([#105](https://github.com/dackota/change-tracking-dashboard/issues/105)) ([a9a28a5](https://github.com/dackota/change-tracking-dashboard/commit/a9a28a523cb029c1744d1d9d53d386daea80109e))

## [0.10.6](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.5...app-v0.10.6) (2026-07-20)


### Bug Fixes

* **deps:** update opentelemetry-go monorepo to v1.44.0 ([#100](https://github.com/dackota/change-tracking-dashboard/issues/100)) ([612943e](https://github.com/dackota/change-tracking-dashboard/commit/612943e26ff5e1467a57b23ae107267c519038c0))

## [0.10.5](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.4...app-v0.10.5) (2026-07-20)


### Bug Fixes

* **deps:** pin docker/dockerfile docker tag to 87999aa ([#87](https://github.com/dackota/change-tracking-dashboard/issues/87)) ([cf90f87](https://github.com/dackota/change-tracking-dashboard/commit/cf90f87408d7c76530c71bf7e6321b5b8657a668))
* **deps:** update module helm.sh/helm/v3 to v4 ([#101](https://github.com/dackota/change-tracking-dashboard/issues/101)) ([cd30f47](https://github.com/dackota/change-tracking-dashboard/commit/cd30f4702c700782507e6cd1147e04d0225dc6c0))

## [0.10.4](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.3...app-v0.10.4) (2026-07-20)


### Bug Fixes

* **deps:** update module golang.org/x/sync to v0.22.0 ([#98](https://github.com/dackota/change-tracking-dashboard/issues/98)) ([e0c62c7](https://github.com/dackota/change-tracking-dashboard/commit/e0c62c7887ede67f75286e46fbd0a746f05cd081))
* **deps:** update module modernc.org/sqlite to v1.54.0 ([#99](https://github.com/dackota/change-tracking-dashboard/issues/99)) ([8ef9a03](https://github.com/dackota/change-tracking-dashboard/commit/8ef9a0388c5981a455cae3ee8fa1588ac4636d93))

## [0.10.3](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.2...app-v0.10.3) (2026-07-20)


### Bug Fixes

* **deps:** update module github.com/zclconf/go-cty to v1.19.0 ([#97](https://github.com/dackota/change-tracking-dashboard/issues/97)) ([2daadda](https://github.com/dackota/change-tracking-dashboard/commit/2daaddafac6074e280dea8db411b17c0e4cb6a7b))
* **deps:** update module helm.sh/helm/v3 to v3.21.3 ([#96](https://github.com/dackota/change-tracking-dashboard/issues/96)) ([e046133](https://github.com/dackota/change-tracking-dashboard/commit/e046133863a146f0b9a2189a65784dc8a9b179a5))

## [0.10.2](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.1...app-v0.10.2) (2026-07-20)


### Bug Fixes

* **deps:** update golang:1.26-alpine docker digest to 0178a64 ([#92](https://github.com/dackota/change-tracking-dashboard/issues/92)) ([dfffac4](https://github.com/dackota/change-tracking-dashboard/commit/dfffac46eb46429bd58de71640f35f25fc358188))

## [0.10.1](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.10.0...app-v0.10.1) (2026-07-20)


### Bug Fixes

* **deps:** update gcr.io/distroless/static:nonroot docker digest to f7f8f72 ([#88](https://github.com/dackota/change-tracking-dashboard/issues/88)) ([76e8093](https://github.com/dackota/change-tracking-dashboard/commit/76e8093ec3d44d5cb423b42f9d3de4a45f0ea955))

## [0.10.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.9.1...app-v0.10.0) (2026-07-08)


### Features

* react to Renovate dashboard/PR checkboxes, swap Trivy for Grype ([#82](https://github.com/dackota/change-tracking-dashboard/issues/82)) ([127e584](https://github.com/dackota/change-tracking-dashboard/commit/127e584f00765c5a8da1acbcb2447dd1669a3dbe))

## [0.9.1](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.9.0...app-v0.9.1) (2026-07-08)


### Bug Fixes

* migrate existing databases missing the issue_refs_json column ([#80](https://github.com/dackota/change-tracking-dashboard/issues/80)) ([31f38e4](https://github.com/dackota/change-tracking-dashboard/commit/31f38e47cdd132110fc4d2e58e910ac510ee3280))

## [0.9.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.8.0...app-v0.9.0) (2026-07-08)


### Features

* add static credential-free plan-diff engine for Terraform ([#76](https://github.com/dackota/change-tracking-dashboard/issues/76)) ([fba4087](https://github.com/dackota/change-tracking-dashboard/commit/fba40878488e624f136f9591285b04e5f7929e4a))
* link changesets to referenced issues from commit messages ([#77](https://github.com/dackota/change-tracking-dashboard/issues/77)) ([e1344c0](https://github.com/dackota/change-tracking-dashboard/commit/e1344c0d3158567012a83ea0ee8a5b06195a0904))

## [0.8.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.7.0...app-v0.8.0) (2026-07-08)


### Features

* classify Terraform changes by Kind and Risk with feed badge ([#75](https://github.com/dackota/change-tracking-dashboard/issues/75)) ([92b9578](https://github.com/dackota/change-tracking-dashboard/commit/92b9578e111c67bdbb5beb18d0956d2db6603720))

## [0.7.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.6.1...app-v0.7.0) (2026-07-08)


### Features

* add HCL extraction backend on the timeline ([feedb81](https://github.com/dackota/change-tracking-dashboard/commit/feedb81f7ebc1072a13dd447a128a29a7e4ca998))
* add HCL extraction backend on the timeline ([2f1e981](https://github.com/dackota/change-tracking-dashboard/commit/2f1e9819375e8079bf59e4e2c9dc8c7b14a3f148))

## [0.6.1](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.6.0...app-v0.6.1) (2026-07-07)


### Bug Fixes

* correct module path to github.com/dackota/change-tracking-dashboard ([3007a50](https://github.com/dackota/change-tracking-dashboard/commit/3007a503f729d3fcf977d00e7393d4711263931b))
* correct module path to github.com/dackota/change-tracking-dashboard ([c63bb27](https://github.com/dackota/change-tracking-dashboard/commit/c63bb272cec615602b0eba54a4432d68004d036f))

## [0.6.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.5.0...app-v0.6.0) (2026-07-07)


### Features

* add OpenTelemetry + RED + structured logging foundation ([#67](https://github.com/dackota/change-tracking-dashboard/issues/67)) ([78db39c](https://github.com/dackota/change-tracking-dashboard/commit/78db39cddcdd8bd36631813189ca2736799b202c))

## [0.5.0](https://github.com/dackota/change-tracking-dashboard/compare/app-v0.4.3...app-v0.5.0) (2026-07-07)


### Features

* add FieldExtractor interface + per-tracker engine selector ([#66](https://github.com/dackota/change-tracking-dashboard/issues/66)) ([4fb83ad](https://github.com/dackota/change-tracking-dashboard/commit/4fb83ad15dbd3c7a7d70c87c9ae9569996584afe))

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
