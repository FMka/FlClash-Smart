# FlClash Smart: Android smart reconnect

Experimental fork of [FlClash](https://github.com/chen08209/FlClash), based on
`8d4f974` (0.9.1). The original GPL-3.0 license applies. The mihomo submodule is
unmodified. This is not an official FlClash release.

Development repository: [FMka/FlClash-Smart](https://github.com/FMka/FlClash-Smart).
See [SMART_ROADMAP.md](SMART_ROADMAP.md) for agreed requirements and device acceptance.

## Using the Android build

1. Install **FlClash Smart** and import your subscription. This build uses a separate
   application ID (`com.follow.clash.smart.dev`) and does not import another client's
   settings automatically. Configure the required application bypass exclusions.
2. Connect. Smart selection is enabled by default; no profile script is required.
   Settings contain the smart switch, preferred country (Any), excluded countries
   (Russia), DNS recovery, daily test-download budgets and the priority-service
   chooser shared with the dashboard. A selected country is a strict restriction.
3. On Wi-Fi/LTE changes the old search is cancelled, old connections are closed,
   and a new search starts. If no node is available, the managed group blocks
   traffic rather than falling back to DIRECT. Existing direct routing rules remain.
4. Selecting a server manually switches smart mode off and saves that choice across
   restarts. Use the visible automatic-mode control to resume. It never starts a VPN
   the user has stopped. REJECT is a blocking route and is hidden from managed choices.

The primary suitable `select` or `url-test` group is inferred from the final MATCH
rule, then a suitable automatic or selection group. Only that selector is flattened
to its leaves; routing rules and other groups are preserved. This supports compatible
mihomo subscriptions, not arbitrary protocols or every possible routing layout.
Advanced profiles can explicitly opt other groups in with `smart-reconnect: true` or
opt a group out with `smart-reconnect: false`. Optional timing parameters remain:

```javascript
const main = (config) => {
  for (const group of config['proxy-groups'] || []) {
    if (group.type === 'url-test' && group.name === 'Auto') {
      group['smart-reconnect'] = {
        'latency-window-ms': 15,
        concurrency: 8,
        'health-interval-seconds': 30,
        'optimize-interval-seconds': 300
      };
    } else if (group.type === 'url-test') {
      group['smart-reconnect'] = false;
    }
  }
  return config;
};
```

This example is optional. The app writes its global settings after profile scripts;
the global disabled switch takes precedence over legacy script opt-ins.

## Algorithm and limits

- The Go core runs the controller; keeping the Flutter screen open is unnecessary.
- Up to eight probes run together, with a three-second latency deadline, bounded
  country/service qualification and a 24-second search budget. A dynamic pool of
  up to twenty nodes per network is refreshed every two minutes on Wi-Fi and five
  minutes on mobile. Failed nodes leave the active pool but retain history, and
  discovery rotates beyond the pool and the beginning of the subscription.
- Candidates must have a known exit country determined through that specific
  node. Russia is excluded by default. Admission uses the
  verified exit rather than a name heuristic: the preferred country and exclusions
  both apply, exclusions win, and unknown country fails closed. The ingress address
  is not treated as the exit. Admission results expire after ten minutes on Wi-Fi
  or thirty minutes on mobile. Country inventory is filled progressively during
  running Smart work, independently of service admission, and retains bounded
  cached discoveries. Importing a profile does not start VPN or a discovery scan.
  Country settings constrain automatic VPN exits, not domestic bypass destinations,
  DNS-provider location or an explicit manual server selection.
- The first qualified node restores routing immediately. Results are collected for
  another second, then up to three candidates within minimum latency +15 ms receive
  serial, bounded download probes (512 KiB each, three seconds). The test endpoint
  is Cloudflare's HTTPS speed endpoint; TLS, HTTP status, content type and body length
  must be valid. Redirects and HTML restriction pages are rejected.
- Measured download results are reused for ten minutes on Wi-Fi or thirty minutes
  on mobile in the same network, with their measurement time exposed in status.
  Normal optimization runs every five minutes. A healthy node is held at least one
  minute. When a fresh positive speed measurement exists for the healthy current
  node, a change requires a 20% speed improvement. Failure recovery bypasses
  the hold. A download probe failure falls back to measured latency.
- The current node is checked every fifteen seconds. When all candidates fail,
  retries back off from three to sixty seconds. During inactivity the engine defers
  full service checks and optional optimization/pool/country work. The existing
  idle wait samples activity and runs one short current-node latency check every
  two minutes without a native wakeup alarm; a failed check permits bounded
  recovery without optional speed downloads. Real
  traffic and existing wake/network/manual-check events resume necessary checks.
  Internal diagnostics do not count as user activity. A quiet transport failure
  may remain undetected until the next sparse check or a relevant event; Doze can
  delay that check. Network changes
  restart recovery. Idle transport failures do not repeatedly restart the VPN.
- Successful full service checks can be reused for thirty seconds on Wi-Fi and
  sixty seconds on mobile. Failed checks are never cached, so confirmation retries
  during a service outage stay fresh. Candidate admission always checks services.
- YouTube, Google, ChatGPT and Telegram are the default priority services. One
  service failing starts a fifteen-second confirmation window and a concurrent
  node-pinned replacement search while retaining the current route. Recovery in
  that window cancels the switch. A replacement must pass all selected services;
  no suitable replacement leaves an explicit degraded state rather than endless swaps.
- Ordinary YouTube is checked using a bounded homepage response, not Premium.
  This is not a proof of sustained video playback. Telegram performs an unauthenticated
  MTProto nonce exchange; it is stronger than checking its website but does not
  prove an authenticated account or calls will work.
- Speed-test downloads have a daily UTC budget: 20 MiB on mobile and 100 MiB on
  Wi-Fi by default. Partial and cancelled response bytes count. When exhausted,
  speed downloads stop while short health probes continue; diagnostic response
  bytes have a separate counter. These are application payload counters, not an
  exact carrier bill including protocol overhead, and do not cap user traffic.
- Screen wake, unlock, Doze exit and app resume request a coalesced health check.
  Persistent transport failure first resets network connections. Only an unresolved
  transport failure may request a native VPN restart, at most once per five minutes.
  The existing Android lifecycle owner handles that restart; Stop and newer requests
  take precedence. A partial service failure never requests a VPN restart.
- History is bounded and stored in the app's private `smart-history.json`; network
  and group keys are hashed. The network fingerprint uses transport, interface and
  DNS, without reading Wi-Fi SSIDs or requesting location. Different Wi-Fi networks
  with the same interface/DNS can share history; nodes are still probed again.
- Only leaf proxies are benchmarked; nested groups are excluded from candidates.
  Large subscriptions are sampled within the time budget: this is not an exhaustive
  global fastest-node guarantee. Small downloads estimate speed to one endpoint;
  they do not establish maximum sustained speed or availability of every service.
- A separate private bounded journal (`smart-journal.log` plus one rotated file)
  survives the general debug log ring and can be exported from smart settings.
  Status exposes the actual selected node, mode, country, history age and budget.
  A running Android VPN service alone does not
  prove internet access. Device background restrictions still apply.

## DNS recovery and routing defaults (v3)

DNS recovery is on by default and has an independent opt-out. It works while the
VPN service runs, including manual server mode and a detached Flutter UI, without
requiring debug logs. Repeated actual public-name resolution timeouts trigger one
bounded diagnostic episode shared with Smart transport work. DNS and VPN failures
remain separate diagnoses; user Stop and newer network/configuration generations
invalidate pending work.

Alternatives use HTTPS with validated certificates and independent pinned-IP
bootstrap. The pool contains Google, Cloudflare, Quad9 No Threat Blocking and
AdGuard Non-filtering. Yandex Basic is an additional last candidate for DIRECT
resolution only. Availability on a particular carrier is not assumed. A candidate
must resolve the failed public name and independent controls; promotion is verified
through the effective resolver path. IPv6 settings and original lookup behavior
are preserved.

If the failed name and an independent control both fail on the original transport,
the compatible public DNS branch receives one shared temporary replacement. An
isolated name failure receives an exact-name replacement. This avoids waiting for
separate recovery episodes for a site's public CDN names while retaining exceptions.

Recovery retains explicit domain/filtering policies, private/system DNS scopes,
DIRECT inheritance and configured mihomo fallback semantics. An incompatible
scope reports `policy-preserved` instead of removing those settings. Application-
owned DoH and DNS outside this Core are not observable by the recovery module.
Temporary recovery state expires and is invalidated on network/config changes;
remembered provider preferences are bounded per network and held in memory.
The settings show the last diagnosed scope/provider/reason, not a claim that every
resolver or Internet service is currently healthy.

The profile's supplied routing rules keep their order and personal exceptions
keep precedence. An explicit MATCH alone is still a supplied policy. A servers-only
profile receives a bundled Russian domain bypass preset plus a final VPN route.
The domain snapshot comes from MetaCubeX/meta-rules-dat `category-ru`; its source,
version and timestamp appear in Smart settings. Updates validate the whole list
before replacement, preserve the last valid data on failure, and are attempted
through an available route at most daily (failed attempts back off six hours).
No additional update timer wakes the phone; existing start/recovery events check
whether work is due. Existing groups explicitly opted out of Smart stay manual.

## Building and checking

Follow `.agents/commands.md` for toolchains and full build instructions. Initialize
the pinned public submodule before building. For an ARM64 test APK:

```text
flutter pub get
flutter analyze --no-pub --no-fatal-infos
flutter build apk --debug --split-per-abi --target-platform android-arm64 --no-pub
```

The debug APK is signed with the local Android debug key and is intended for device
testing. Do not install upstream update APKs as updates for this custom variant.

On a Windows build host, Rust bindgen needs a host `libclang.dll` and an explicit
Clang resource directory containing builtin headers. The v3 test build used the
Android NDK 28.2 host libclang and its Clang 19 headers, with `-resource-dir` supplied through
the target's `BINDGEN_EXTRA_CLANG_ARGS`. When project and pub cache are on different
drives, set `kotlin.incremental=false` in the build's Gradle user properties. Keep
Rust/Cargo cache directories explicitly forwarded to subprocesses: Flutter's hook
environment does not inherit every shell variable.

Core checks (from `core/`, with `CGO_ENABLED=0`):

```text
go test ./smart
go test . -run TestSmart
go vet . ./cmd/...
```

The existing Windows baseline test
`TestHandleValidateProxiesLeavesARunningProxysResolverInPlace` has a path/error-text
failure unrelated to reconnect. Record it separately when running `go test .`.

## Required device acceptance

Test the actual subscription and carrier on Android: Wi-Fi -> LTE -> Wi-Fi,
screen off, dead current node, all nodes unavailable, manual selection during a
probe, Stop during search/download, and several rapid network changes. Confirm
Telegram/ChatGPT traffic through the selected VPN node; a bypassed site such as VK
is not sufficient evidence. Portable tests cannot verify Android background policy
or a carrier's restrictions.
