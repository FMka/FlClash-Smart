## ADDED Requirements

### Requirement: Recover confirmed DNS failures

Smart DNS recovery SHALL distinguish resolution failures from VPN transport,
authentication and service failures. It SHALL test alternative resolvers against
the failed hostname and bounded controls before changing the active DNS policy.
It SHALL retain certificate validation, avoid circular bootstrap dependencies,
rate-limit retries and preserve a working configuration on unsuccessful recovery.

### Requirement: Protect ordinary browsing without manual setup

Smart DNS recovery SHALL be enabled by default with a persistent opt-out setting.
It SHALL observe real resolver failures for public names, including names used by
bypass traffic, without requiring a script, debug logging or an open app screen.
It SHALL use the existing Core/Android service ownership and remain subordinate to
user Stop. It SHALL NOT add VK or Sberbank to periodic priority-service probes.

#### Scenario: Public website fails before a connection can be established

- WHEN repeated uncached resolver exchanges time out for a public website
- THEN Smart SHALL diagnose the failed name and bounded independent controls,
  and use a proven compatible alternative when the active DNS path is failing.
- AND it SHALL preserve the website's routing policy and retry resolution without
  classifying every VPN candidate as unavailable or forcing bypass traffic to VPN.

#### Scenario: Only one name or an intentional policy response fails

- WHEN controls resolve but the target has an authoritative failure, a valid
  negative answer, or an explicit local/filtering policy
- THEN Smart SHALL NOT treat that result alone as a general resolver outage,
  discard the policy or disable certificate/DNSSEC validation.

#### Scenario: Network or sleep invalidates an old resolver connection

- WHEN a new network generation or service resume invalidates earlier DNS evidence
- THEN Smart SHALL revalidate the applicable DNS path with bounded work before
  reusing its health result and SHALL ignore stale completions.

#### Scenario: The entire underlying network is unavailable

- WHEN independent connectivity evidence confirms underlying network loss and
  resolver transports cannot be reached
- THEN Smart SHALL report the network failure, back off and retry on network/resume
  signals rather than continuously rotating providers or restarting the VPN.

#### Scenario: Alternatives fail without proof of general network loss

- WHEN all tested resolver transports fail but general connectivity is unproven
- THEN Smart SHALL report DNS recovery unavailable, retain the last valid policy
  and back off without claiming that the entire Internet connection is down.

### Requirement: Keep DNS recovery scoped and bootstrap independent

Recovery SHALL identify whether bootstrap, VPN-node resolution, public destination
resolution or an explicit DNS policy is affected. It SHALL preserve private/local
resolution and intentional policy scopes. Only the affected compatible path SHALL
be changed, with provenance, rollback and a per-network last-working result.
Encrypted replacement resolvers SHALL remain reachable without relying on the
failed resolver or an already-working VPN. Recovery SHALL not require plaintext
UDP port 53 as its sole bootstrap route, nor silently downgrade encrypted DNS.

#### Scenario: DIRECT has a separate resolver policy

- WHEN a profile supplies `direct-nameserver` and a follow-policy setting
- THEN recovery SHALL verify the effective failed resolver path and preserve
  existing policy inheritance, rather than assuming a global nameserver-policy
  applies or enabling inheritance for all direct traffic to repair one domain.

#### Scenario: Alternate resolution restores the current VPN server

- WHEN profile DNS repeatedly fails but an alternative resolves the failed name
- THEN the app SHALL apply the compatible DNS change and recheck the current route
  before performing an unnecessary VPN-server change.

#### Scenario: Resolution succeeds but VPN transport fails

- WHEN a server name resolves correctly and the VPN connection still fails
- THEN Smart SHALL use transport recovery rather than continually rotating DNS.

#### Scenario: Stop or network change supersedes a DNS probe

- WHEN newer intent invalidates a pending recovery
- THEN its late result SHALL neither change the new policy nor start a stopped VPN.

### Requirement: Make DNS changes observable

The app SHALL show the active DNS source/provider, recovery result and bounded
diagnostic history. Probe payload SHALL be included in diagnostic accounting.
Diagnostic records SHALL distinguish DNS timeouts from VPN transport and service
failures. They SHALL NOT claim a final outbound route solely from a DNS initiator
label, or invent an upstream provider when an exchange error omits that field.

### Requirement: Minimize diagnostic energy use

DNS recovery SHALL primarily use real resolver activity and existing Smart network,
resume and health-check events. It SHALL NOT introduce periodic resolver polling,
additional wakeup alarms or a permanent wake lock. DNS and transport recovery
SHALL coalesce related failures into one bounded diagnostic episode, share retry
limits and backoff, and reuse sufficiently fresh evidence only for the same
network generation and applicable resolver path. Error bursts SHALL NOT bypass
cooldown. Cache expiry alone SHALL NOT wake the device or trigger a full scan.

#### Scenario: The phone is idle without user traffic

- WHEN no new query, network change or existing recovery event requires work
- THEN DNS recovery SHALL NOT generate background probes solely to keep its
  status or cached measurements fresh.

#### Scenario: A real request fails with the screen off

- WHEN an actual resolver exchange fails while the VPN service is running
- THEN necessary bounded recovery SHALL remain eligible with the screen off,
  without scheduling optional country, speed or full-pool measurements.

#### Scenario: An idle failure has no observable signal yet

- WHEN a remote failure occurs without requests or a relevant network event
- THEN Smart MAY detect it on the next real request or resume event and SHALL
  NOT claim continuously verified availability while performing no probes.
