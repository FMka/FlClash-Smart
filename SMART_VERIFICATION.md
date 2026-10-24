# Smart reconnect v2 verification

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
