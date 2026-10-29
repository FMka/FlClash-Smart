# Fresh 0.9.2 / Smart V3.2 verification

Date: 2026-10-29. First parent is official stable FlClash 0.9.2
`7fd417fb9a9a277c6a9339a8ac87f907df7c25f7`; Smart feature source is the archived
V3.1 `d0e5f54`. The rejected V3.1 installation was not diagnosed or repaired.
Official mihomo remains `c5062921054902b25215a6a933e72ccd714c9bfa` unmodified.

The verified source merge `145d72c092c19f978f7343ac2eb782e303d1eeeb` is published
on `main` and `codex/fresh-v092-smart`; previous V3.1 is on `codex/archive-v31`.
The replacement APK is available in the
[V3.2 prerelease](https://github.com/FMka/FlClash-Smart/releases/tag/smart-v3.2-20261029).
GitHub reports the same SHA-256 as the local artifact and its public download
responds successfully. Subsequent documentation bookkeeping does not change the
compiled production sources. Former PR #1 is confirmed closed and merged.

## Current implementation and evidence

All agreed Smart functionality through V3.1 is retained on the fresh source:
default-on/manual control, country priority/fallback/exclusions, complete separate
Wi-Fi/LTE pools, common-current handoff, service-aware recovery, scoped DNS recovery,
subscription-routing precedence, Russian bypass fallback, budgets and idle limits.
V4/V5 free-server experiments remain inactive. Automatic application updates and
unsolicited prompts remain removed.

First-run agreement acceptance now leads to the standard subscription-import offer.
New subscriptions receive isolated bounded response/country discovery with progress,
queueing and cancellation. Neither an inactive profile nor an off VPN is activated.
Pool namespaces include profile identity; discovery never authorizes selection.

- 791 covering Flutter tests passed after source integration. Coverage includes
  import queue/cancellation/retry, first-run offer, agreement without typing,
  profile import actions, model migrations, complete pools, protocol/controller,
  setup/lifecycle, country settings, DNS/routing, dashboard and localization.
- Flutter dependency resolution, model/provider and locale generation passed.
  Final `flutter analyze --no-pub --no-fatal-infos` reports no issues.
- 135 Android JVM tests passed (common 30, service 26, app 79), together with a real
  ARM64 gomobile bind and Kotlin compilation. Final APK native provenance is checked
  again after the final Go edits; the test run itself does not prove phone install.
- Root/Smart Go suites passed, engine ten-repeat passed, host and Android ARM64
  `with_gvisor` vet passed. Nine isolated-import regressions include real loopback
  HTTP CONNECT, unchanged applied proxy map with VPN off, profile/transport scope,
  provider identity/unsupported transforms, concurrent first apply, cancellation,
  newer-fingerprint protection and response retention with a cancelled country lookup.
- Two original Windows-only Go harness limitations are excluded: path/error-text
  assertion `TestHandleValidateProxiesLeavesARunningProxysResolverInPlace` and open
  `cache.db` during TempDir cleanup in
  `TestHandleGetProxyDefinitionReadsAProviderProxyAsItsProviderBuildsIt` (its body
  assertions pass). Neither test's production behavior was changed for this fork.
- Scoped structural review of configuration, lifecycle ownership, isolated adapters,
  RPC, UI queue and pool persistence found no qualifying findings. Functional findings
  about first-apply cancellation, transformed providers and slow geography were
  corrected and covered by regression tests.

The exploratory full root Flutter run was not green: it included unavailable Windows
editor native DLL tests and path-separator-dependent upstream lint checks, as well
as old agreement/branding/layout expectations corrected by the covering run. It is
not represented as a complete-suite pass. Its raw log is retained locally at
`tmp/fresh-v092-flutter-tests.log`; focused final checks are in
`tmp/fresh-v092-covering-flutter-tests.log`.

Release-mode APK packaging/signing passed: `FlClash-Smart-arm64-v3.2-test.apk`,
61,181,138 bytes, SHA-256
`48d53cae13c28dde43aced0a1a5952fe992716ea8fc5a1acf9819602e8eddb68`.
Package `com.follow.clash.smart.dev`, version `0.9.2`, Android code `2026104902`,
minSDK 24 / targetSDK 37, ARM64 only, not debuggable. V2 signature and the previous
certificate `65af5c9da896e2d111844aee64201c22eab987195d7a0d387ae6df425307cf81`
match. CRC, zipalign 16 KiB and all ELF PT_LOAD alignments passed. Packaged native
Core matches the stripped release output; the final AAR is newer than all final Go
edits. Metadata is in `output/smart-reconnect/verification-v3.2-apk.json`.

No host VPN was started. Physical installation, EULA/import experience on Android,
real subscription connectivity, Wi-Fi/LTE, long Doze and battery consumption remain
device acceptance tasks; static signature/alignment checks cannot establish those.

---

# Prior V3.1 verification

Date: 2026-10-29. Based on published V3 (`733ae26`), Android debug ARM64,
`0.9.1+2026102901`, package `com.follow.clash.smart.dev`.
Verified ABI version code: `2026104901`; final APK verification is recorded in
`output/smart-reconnect/verification-v3.1.json` in the workspace.

## V3.1 changes and checks

- Complete persistent Wi-Fi and mobile pools retain every responding unchanged
  node, including previous response evidence after a later failure. The former
  20-node pool cap and age expiry no longer discard working history. Negative-only
  discovery remains bounded; optional refresh remains batched and activity-aware.
- Candidate selection still requires a fresh node-pinned response and country/
  service admission. Pool membership is scheduling evidence, not an admission.
- Preferred country is a priority: other allowed countries are fallback, with
  return after a preferred-country node passes. Excluded countries remain strict.
- An unchanged current node saved in both transport pools remains selected at
  handoff and is checked immediately. Network sessions may still reconnect.
- Pool screens fetch complete lists explicitly; routine status carries counts.
- V4/V5 free-server changes are absent. A stored retired built-in placeholder
  cannot remain selected; personal subscriptions and stored rows are retained.
- Automatic application update checks, toggle and startup prompts are removed.
  Manual About checking and subscription/rule updates remain available.
- `flutter pub get --offline`, code generation for models/providers/locales and
  `flutter analyze --no-pub --no-fatal-infos` passed; dependency lock is unchanged.
- 492 covering Flutter tests passed, including full lazy pool lists, legacy
  settings, retired profile filtering, protocol summary/bulk arguments and updater
  behavior, alongside existing DNS/routing/lifecycle/localization checks. Native
  build hooks were restored before the final APK build.
- Root and engine Go suites passed with CGO disabled, excluding only the same
  pre-existing Windows path/error-text assertion documented under V3 below.
  The final engine suite also passed ten consecutive runs.
- 135 Android JVM tests passed across common, service and app modules. Formatting,
  comment-density and `git diff --check` passed for the final source snapshot.
- Host vet and Android ARM64 vet with `with_gvisor` passed. No mihomo submodule
  changes: `8380d0e7462f061a964323e9bf3129a236fcc5d5`.
- Scoped pre-commit structural review covers pool/history ownership, country
  scheduling, handoff selection, bulk status and UI/updater boundaries. No
  qualifying responsibility or dependency findings were identified.

The APK is 131,913,390 bytes, SHA-256
`38baa26a54cd2ba42eb97e39b3e56a1db25fccaedd3a92ae6485f1178a6f0f63`.
V2 signature verification passed; the certificate matches V3/V4/V5 and the version
code is higher. Every native library is ARM64 ELF; ZIP CRC checks passed, the native
core matches the final stripped build and was rebuilt after the last Go edits.
Free-server assets are absent; the maximum packaging gap is 4096 bytes.

Final source and packaging evidence are recorded beside the APK. Physical handoff,
long Doze, carrier behavior and battery consumption
remain phone acceptance tasks. No host VPN was started for these checks.

---

# Prior V3 verification

Date: 2026-10-28. Android debug ARM64, version `0.9.1+2026102801`.
Package `com.follow.clash.smart.dev`, Android ABI version code `2026104801`.

## Changes and automated checks

The v3 change adds strict preferred/excluded exit-country controls, progressive
country inventory, default-on scoped DNS recovery, routing-source provenance and
a validated cached Russian bypass fallback for servers-only profiles. Idle work
retains a sparse essential current-node check and defers optional probes.

- `flutter pub get` passed; dependency lock is unchanged. Code generation was run
  for settings/models and all seven locales; generated output was autoformatted.
- `flutter analyze --no-pub --no-fatal-infos` passed without issues.
- 402 covering Flutter tests passed: model migration, settings/status, profile and
  routing provenance, DNS override preservation, routed probes, lifecycle actions,
  protocol contract, localization and UI lint. Native build hooks were disabled
  only for Dart tests and restored byte-for-byte before the APK build.
- 135 Android JVM tests passed across common, service and app modules.
- Full root and engine Go suites passed with CGO disabled, excluding only the
  same pre-existing Windows path/error-text assertion recorded below. The baseline
  test was separately reproduced and was not changed by v3.
- Host vet for `. ./smart ./cmd/...` and Android ARM64 vet with `with_gvisor` for
  `. ./smart ./mobile ./tun ./platform` passed.
- Repeated DNS tests cover detached-UI DIRECT recovery, inaccessible bootstrap,
  validated TLS, original lookup/IPv6 behavior, effective resolver promotion,
  shared public-branch versus exact-name recovery, policy/private preservation,
  private HTTPS IPv4/IPv6 endpoints, cooldown and Stop/network cancellation.
- Engine tests cover sparse idle checks, cached-DNS/dead-node recovery without
  activity notifications, diagnostic serialization and strict country conflicts.
- Routing tests cover explicit MATCH, personal exceptions, manual opt-out,
  reloadable generated defaults, legacy settings, rejected malformed updates,
  last-valid data and Stop discarding a late refresh.
- Scoped structural review found no qualifying responsibility/dependency findings.
  Functional review found two P2 issues (idle failed-Dial detection and private DoH
  host classification); both were corrected and covered before the final build.
- `core/Clash.Meta` is unchanged at
  `8380d0e7462f061a964323e9bf3129a236fcc5d5`.

The APK signature/package/ABI and native-library checks are recorded beside the
delivered APK in `verification-v3.json`. It uses the same debug signing certificate
as v2 and can update that installation.

## Device acceptance still required

Check the real subscription on Wi-Fi and LTE, long Doze, carrier restrictions,
country filtering with no matching exit, real bypass DNS failures, manual mode,
Stop during recovery, service access and diagnostic traffic. No battery saving,
video playback or long-term reliability claim follows from the automated checks.
No host VPN instance was started and no host DNS/network settings were changed.

## Previous v2 verification

Date: 2026-10-24. Android debug ARM64, version `0.9.1+2026102401`.

The change enables built-in smart selection, admission by exit country and selected
services, a rotating per-network pool, persisted traffic budgets and manual mode,
and Android wake/transport recovery. See `SMART_RECONNECT.md` for behavior and limits.

## Automated checks

- 389 covering Flutter tests passed: configuration migration, smart settings/status,
  service selection, profile apply, proxy actions, route/status cancellation, dashboard,
  native protocol contract, localization and lifecycle presentation. Build hooks were
  disabled only for Dart-side tests and restored byte-for-byte before the APK build.
- 135 Android JVM tests passed across common, service and app modules, including
  Stop/new Start during recovery, stopped/proxy-only refusal, revoked permission,
  restart cooldown and a failed restart releasing its run intent.
- `CGO_ENABLED=0 go test ./smart . -skip
  '^TestHandleValidateProxiesLeavesARunningProxysResolverInPlace$'` passed.
  The excluded Windows path/error-text assertion was separately identified in the
  original version; it is not a smart reconnect regression.
- Host `go vet ./smart . ./cmd/...` and Android ARM64 `go vet -tags=with_gvisor
  . ./mobile ./tun ./platform` passed with CGO disabled.
- The ordinary YouTube checker has local pinned HTTPS tests for a valid page,
  incidental Premium denial, captcha/empty response, HTTP denial and a regional
  block. Telegram handshake tests reject a mismatched nonce.
- `core/Clash.Meta` stays pinned at `8380d0e7462f061a964323e9bf3129a236fcc5d5`
  and is unmodified.

## Review and remaining acceptance

The structural review covered the engine/runtime boundary, settings injection,
live selection status and the Android broadcast/lifecycle path. Recovery uses the
existing authoritative native intent and transition lock. Cancellation, generation
guards and callback cleanup were checked before the final build.

Actual Wi-Fi/LTE handoff, long Doze, carrier restrictions, vendor battery policy and
long-term stability remain device acceptance tasks. A short service probe does not
prove video playback, authenticated account access or calls. Diagnostic counters
measure application payload rather than exact on-wire carrier charges.
