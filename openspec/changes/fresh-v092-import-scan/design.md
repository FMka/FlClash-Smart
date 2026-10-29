# Design

Start the integration branch at the exact upstream stable tag and merge the saved
Smart changes with reviewed conflicts. Fresh process-wide Android NetworkObserver
remains authoritative. Scan callbacks never become a VPN lifecycle owner.

The Flutter import coordinator builds each imported profile through existing
profile preparation without applying it. It serializes newly imported profiles,
tracks native scan IDs, cancels obsolete work and shows progress in Profiles.
The Core receives prepared base/overlay or full configuration, creates isolated
supported proxy adapters and performs bounded node-pinned response/country probes.
It never mutates global routing/providers or calls selection. Unsupported chain/
provider cases are reported and may be checked under normal active-profile work.

Network, newer scan and user Stop invalidate pending scan tokens. Ordinary
configuration application does not cancel an isolated scan, so activation of the
first imported subscription cannot interrupt its own discovery. Newer node
fingerprints cannot be overwritten by an older in-flight discovery snapshot.
Results are committed only for matching node identities and the network observed
at the start; only the observed Wi-Fi/mobile pool is updated. Speed timestamps
are not fabricated. Selection still needs fresh country/service qualification.

Each import pass has fixed deadlines, reduced mobile concurrency and a response
byte budget. It adds no periodic background alarm, no speed downloads and no VPN
start. Large lists remain partial when the pass budget is exhausted and are
progressively discovered by existing Smart work.

Build a fresh release APK from the new source and original native pin, retain the
custom package/signing identity for updates and increase the Android version code.
Desktop checks do not establish physical-phone install or battery acceptance.
