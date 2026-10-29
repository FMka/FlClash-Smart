# V3: complete saved pools and quiet startup

Status: implemented and desktop-verified; phone acceptance pending. Base: `733ae26` (V3).

Return to V3 without the uncommitted V4/V5 free-server features. The removed work
is preserved outside the repository in `migration/v4-v5-before-v3-return-2026-10-29/`.
Keep existing private subscriptions, DNS recovery, exclusions and battery limits.

Store every responding node in separate persistent Wi-Fi and mobile pools, without
the old 20-node pool / 128-success history limits or 30-minute pool expiry. Refresh
progressively; saved evidence never substitutes for a current selection check.

The user's follow-up makes preferred country a priority, with fallback to other
non-excluded countries. Check saved preferred nodes first, then discover preferred
nodes; return to a preferred node when it passes current qualification again.
If the current node is saved in both transport pools, keep its route during a
Wi-Fi/mobile handoff and verify asynchronously. Failure triggers replacement.

Remove automatic application-update checks, their setting and unsolicited dialogs.
Manual checking remains explicit. Subscription/rule updates are independent.
