# Design notes and verified sources

Date: 2026-10-28. Implemented v3 design; device acceptance remains pending.

## Existing integration

`lib/models/clash_config.dart` defaults to rule mode and provides baseline DNS when
the profile has no enabled DNS. That baseline currently names doh.pub and AliDNS.
`lib/common/task.dart` retains imported rules and prepends personal added rules.
DNS overrides currently use the profile-apply path; automated failover must avoid
needlessly resetting healthy Smart selection or creating another lifecycle owner.

## Country controls

Use a single optional preferred ISO country code (Any when empty) and a set of
excluded ISO country codes (RU by default). Admission is the intersection of the
preferred-country restriction, excluded-country filter and existing qualifications.
The exclusion takes precedence on conflicting restored settings; report that
conflict rather than resetting the preference or weakening the exclusions.

Migrate existing saved `smartAllowRussian` as follows: true becomes no initial
exclusions, false becomes RU excluded. New installations default to RU excluded.
Retain stored exclusions when countries temporarily disappear from a subscription.

Keep country discovery separate from Smart admission: a reachable exit can belong
to a discovered country even when current service filters exclude it. The preferred
country and exclusions constrain VPN exits; the Russian bypass preset classifies destination
sites. These settings do not replace each other. Country discovery must be bounded
and progressive on mobile networks, with a visible unresolved count.

## Resolver pool

| Service | IPv4 pair | DoH |
| --- | --- | --- |
| Google Public DNS | 8.8.8.8, 8.8.4.4 | https://dns.google/dns-query |
| Cloudflare standard | 1.1.1.1, 1.0.0.1 | https://cloudflare-dns.com/dns-query |
| Quad9 no threat blocking | 9.9.9.10, 149.112.112.10 | https://dns10.quad9.net/dns-query |
| AdGuard non-filtering | 94.140.14.140, 94.140.14.141 | https://unfiltered.adguard-dns.com/dns-query |

Official endpoint sources: [Google](https://developers.google.com/speed/public-dns/docs/doh),
[Cloudflare](https://developers.cloudflare.com/1.1.1.1/ip-addresses/),
[Quad9](https://docs.quad9.net/services/),
[AdGuard](https://adguard-dns.io/en/public-dns.html).
These endpoints were checked against documentation; user-network availability was not tested.

Mihomo separates bootstrap (`default-nameserver`), proxy-node resolution
(`proxy-server-nameserver`) and direct resolution (`direct-nameserver`). Its fallback
has geo-filter semantics and is not by itself the proposed diagnostic state machine.
See [official DNS documentation](https://wiki.metacubex.one/en/config/dns/).

## Referenced VK incident and recovery design

The user referenced the chat "Скрипт правил VK" on 2026-10-28 and subsequently
supplied its pasted text. The text and four screenshots show repeated roughly
five-second DNS exchange timeouts for
`m.vk.ru` and `st1-56.vk.ru`, plus an unanswered UDP query to `8.8.8.8:53` for a
separate public name. This confirms failed resolution in that episode; it does not
establish the precise transport/network cause or a complete active DNS configuration.
The empty override screen and editor values are not runtime-policy evidence.
The pasted conversation ends with advice to add `77.88.8.8` to the general
nameserver list and later scope a policy to VK. It contains no confirmed successful
test. The earlier assistant's inference that editor defaults prove active DNS is
not adopted. No claim about a successful final fix is made here.

Failure before name resolution can explain why no later connection to VK appears
in the requests page. The DNS initiator label `direct` identifies a resolver call
path, not the final outbound selected for an application connection.

The existing observer has usable structured fields: domain, type, time, delay,
initiator, upstream, cache, RCODE and error. A failed exchange may omit the upstream;
configuration provenance and scoped diagnostic probes are therefore also needed.
Use the wrapper's existing `dns.DefaultQueryNotify` hook for an internal consumer
before the UI-listener guard. Flutter's DNS list is only presentation: detached
Flutter listeners are removed and current DNS UI events are not retained for it.
Recovery must run alongside the existing Go Smart owner while the Core is running,
without adding patches to the pinned mihomo submodule or requiring debug logs.
Do not reuse Flutter's broad `isFailed` UI predicate as the recovery trigger:
NXDOMAIN and intentional policy responses are not resolver outages. Deduplicate
singleflight/background retries and validate useful answers during diagnosis.
The observer does not cover application-owned DoH or DNS outside the Core, and
does not identify the active network/configuration generation by itself.
`NewResolver` builds a separate DIRECT resolver when `direct-nameserver` is set;
it inherits the main policy only when `direct-nameserver-follow-policy` is true.
Adding a global `nameserver-policy` therefore may not affect the failed path.
Do not flip that flag automatically: it would change every existing policy's
application to direct traffic. Prove the recovery overlay reaches the implicated
resolver and test both flag values with an explicit DIRECT nameserver.

Implemented flow:

1. Coalesce repeated uncached exchange failures into one bounded diagnostic cycle.
   A cache hit, intentional negative answer or local/filter policy is not an outage.
2. Test the affected public name and small independent controls through the
   implicated policy path and compatible encrypted alternatives. Track bootstrap,
   VPN-node and destination failures separately; do not send private names to the
   public pool or silently remove an explicit filtering policy.
3. Promote a proven compatible resolver only for the failed scope, preserving
   routing and user overrides. Do not move VK from bypass to VPN to mask DNS failure.
   Preserve the imported profile, record a temporary recovery overlay and retain
   rollback state. Verify success through the effective path after applying it.
4. Recheck the existing VPN route if it depended on the failed DNS path; server
   reselection remains a separate decision. Avoid a full-profile reload when an
   existing narrower integration can safely apply the DNS change; verify that
   capability during implementation instead of assuming a live setter exists.
5. Remember healthy resolver transports per network with bounded age. Network/resume
   signals invalidate stale evidence and in-flight operations. Underlying network
   loss triggers backoff; user Stop cancels recovery and late callbacks cannot start it.
6. Surface the active DNS provider/source and a concise recovery reason. Keep bounded
   diagnostic accounting and history without logging subscription credentials.

The public default should prefer encrypted transports and avoid depending solely
on ordinary UDP-53 bootstrap. Bootstrapping by an independent reachable path or a
provider-supported IP endpoint must retain TLS identity validation; see
[RFC 8484 section 10](https://www.rfc-editor.org/rfc/rfc8484.html#section-10).
The initial provider order and retry thresholds remain tuning choices, not a
claim of measured reliability on the user's carrier.

The suggested `77.88.8.8` belongs to Yandex DNS Basic; its official service page
also documents encrypted DNS service names and ports. Consider that provider as
an additional candidate for public DIRECT/bypass resolution, subject to the same
reachability and policy checks, rather than making it an unconditional global
replacement. See [Yandex DNS](https://dns.yandex.com/). Choosing a Russian DNS
provider for direct public traffic is independent of excluding Russian VPN exits.
No assertion of carrier reachability is made for this candidate.

## Energy and shared diagnostic work

The user's 2026-10-28 battery requirement applies to all Smart features. Prefer
passive real-traffic evidence, existing health checks and network/resume callbacks.
DNS recovery adds no polling loop, device-wakeup alarm or permanent wake lock.
Related DNS, transport, handoff and resume failures share one bounded diagnostic
episode; a burst from several apps must not multiply probe work or reset cooldown.
Reusing observations requires matching network/configuration generation and DNS
scope, not merely a successful unrelated service check.

Remember working candidates per network; evaluate expiry and backoff on existing
events rather than a new wakeup timer. Defer optional speed tests, country discovery
and bulk pool refresh during idle periods, especially on mobile data. Turning the
screen off alone must not block recovery of actual traffic or imply that streaming
and other background connections have stopped. Required current-route monitoring
remains adaptive and coalesced; the new DNS feature adds no duplicate heartbeat.

In a quiet idle period, the existing two-minute wait retains one short current-node
latency check. It does not run full service, country, speed or pool probes while
healthy; an actual failure permits bounded recovery. No new native alarm is added.
Deep device sleep may defer execution until Android schedules the service again.
Record bounded counters for diagnostic episodes, probe count/bytes and reasons;
do not require high-volume debug logging for normal operation. Before claiming a
battery improvement, compare idle and active behavior on the same real phone and
network, including Doze, with actual recovery still covered.

## Rule-data candidates

- [v2fly/domain-list-community](https://github.com/v2fly/domain-list-community):
  community-maintained domain categories with reviewed contributions. Its
  [category-ru](https://github.com/v2fly/domain-list-community/blob/master/data/category-ru)
  includes Sber and the VK/Mail.ru group categories.
- [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat):
  distribution of geosite/geoip data and mihomo-compatible rule sets; already named
  by this fork's default geodata URLs. Dataset coverage is not an automatic global
  policy deciding which sites should use VPN for every user's country.
- [itdoginfo/allow-domains](https://github.com/itdoginfo/allow-domains): public
  maintained lists in mihomo-compatible formats. `Russia inside` concerns resources
  requiring access around restrictions from Russia; `Russia outside` concerns
  Russian resources restricted to Russian source networks. Neither is a blanket
  domestic DIRECT list. Verify applicable licensing before embedding third-party data.

## Acceptance before build

Exercise subscription rules present/absent, generated catch-all provenance, stale
rule update, country selection with no candidates, DNS-only vs transport failures,
bootstrap without VPN, Stop and rapid Wi-Fi/LTE changes. Automated checks do not
replace device/operator validation.
Reproduce the VK class of failure with unreachable UDP-53 DNS, a reachable
encrypted alternative, unchanged bypass rules and a detached UI. Also cover no
network, all alternatives failing, authoritative NXDOMAIN, local/split DNS,
intentional filtering, resume after inactivity and cancellation during apply.
