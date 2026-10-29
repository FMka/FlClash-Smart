# Design

Use the existing Go history owner and atomic file persistence. Pool keys use
transport type and managed group; measurement context retains the actual network
fingerprint so Wi-Fi changes cannot reuse a fresh-looking speed from another link.
Existing exact-network history is imported when that network is next observed.

Retain successful response evidence and the latest availability separately from
country/service qualification. Remove changed/deleted definitions when the current
subscription is enumerated. Failed never-working discovery entries remain bounded;
they cannot evict previously responding nodes. No new timer or mass speed test.

Prioritize cached country evidence solely for scheduling. Real exit/service
qualification remains required before a new route is selected. Exclusions stay
absolute. A valid preferred result outranks ping/speed hold rules; other-country
fallback remains available while preferred nodes fail.

Maintain the current selector across handoff only for an unchanged node recorded
in both saved pools. Start its existing health check immediately, without an
intermediate REJECT. Normal cold-start safety, Stop and generation guards remain.

Expose both complete saved lists in Smart settings using lazy list rendering.
Regular status RPCs carry counts, not bulk lists; full snapshots are explicitly
requested only when a pool page is opened or pulled to refresh. Provider refresh
identities use each node's content, so an unrelated sibling cannot erase its pool
evidence. A latency reply is recorded before slower qualification, even if that
qualification is subsequently cancelled; it still cannot select that node.
Regenerate settings/localizations after removing the app-update preference.
