# FlClash Smart: Android smart reconnect

Experimental fork of [FlClash](https://github.com/chen08209/FlClash), based on
`8d4f974` (0.9.1). The original GPL-3.0 license applies. The mihomo submodule is
unmodified. This is not an official FlClash release.

Development repository: [FMka/FlClash-Smart](https://github.com/FMka/FlClash-Smart).
See [SMART_ROADMAP.md](SMART_ROADMAP.md) for planned changes and device findings.

## Using the Android build

1. Install **FlClash Smart** and import your subscription. This build uses a separate
   application ID (`com.follow.clash.smart.dev`) and does not import another client's
   settings automatically. Configure the required application bypass exclusions.
2. Choose the subscription's automatic selection group in the proxy screen, then
   connect. The fork manages the first `url-test` group by default; other automatic
   groups retain their upstream behavior to avoid background speed tests per country.
   A parent manual group must point to that automatic group, rather than to one node.
3. On Wi-Fi/LTE changes the old search is cancelled, old connections are closed,
   and a new search starts. If no node is available, the managed group blocks
   traffic rather than falling back to DIRECT. Existing direct routing rules remain.
4. A manual selection inside a managed group pauses its automation until the next
   VPN start or profile reload. A manual selection in a parent group continues
   using that selected route.

For another `select` or `url-test` group, opt in with `smart-reconnect: true`.
An explicitly managed group suppresses the default for following groups. To retain the upstream
`url-test` behavior, set `smart-reconnect: false` on that group. Parameters can also
be supplied through a profile script:

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

Replace `Auto` with the chosen group's exact name. A profile that has no suitable
`url-test` group needs explicit activation on its `select` group. The example
above must also change its type condition to `group.type === 'select'` in that
case. Neither a node named Auto nor selecting REJECT enables smart selection;
selecting any member of a managed group pauses automation.

## Algorithm and limits

- The Go core runs the controller; keeping the Flutter screen open is unnecessary.
- Up to eight latency probes run together, with a three-second deadline each and
  twelve seconds per search. Previously successful nodes for the network are tried
  first. The remaining search samples the beginning, end and distributed positions
  across the list, rotating between attempts.
- The first working node restores routing immediately. Results are collected for
  another second, then up to three candidates within minimum latency +15 ms receive
  serial, bounded download probes (512 KiB each, three seconds). The test endpoint
  is Cloudflare's HTTPS speed endpoint; TLS, HTTP status, content type and body length
  must be valid. Redirects and HTML restriction pages are rejected.
- Measured download results are reused for five minutes in the same network.
  Normal optimization runs every five minutes. A healthy node is held at least one
  minute. When a fresh positive speed measurement exists for the healthy current
  node, a change requires a 20% speed improvement. Failure recovery bypasses
  the hold. A download probe failure falls back to measured latency.
- The current node is checked every thirty seconds. When all candidates fail,
  retries back off from three to sixty seconds. Network changes restart recovery.
- History is bounded and stored in the app's private `smart-history.json`; network
  and group keys are hashed. The network fingerprint uses transport, interface and
  DNS, without reading Wi-Fi SSIDs or requesting location. Different Wi-Fi networks
  with the same interface/DNS can share history; nodes are still probed again.
- Only leaf proxies are benchmarked; nested groups are excluded from candidates.
  Large subscriptions are sampled within the time budget: this is not an exhaustive
  global fastest-node guarantee. Small downloads estimate speed to one endpoint;
  they do not establish maximum sustained speed or availability of every service.
- Logs prefixed `[SmartReconnect]` expose searching, measuring, connected,
  unavailable and switching reasons. A running Android VPN service alone does not
  prove internet access. Device background restrictions still apply.

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
Clang resource directory containing builtin headers. The test build used the
PyPI libclang 18.1.1 runtime and NDK headers, with `-resource-dir` supplied through
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
