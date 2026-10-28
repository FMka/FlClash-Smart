# Smart reconnect v3 verification

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
