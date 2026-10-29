# Full first scan and faster working discovery

Status: requirements recorded for the next build, 2026-10-29; not implemented.
Supersedes V3.2's bounded initial-pass completion limits, not its shipped description.

After adding a subscription/proxy list, refresh its complete effective node inventory
and scan every node for response and actual exit-country evidence. Allocate one
shared initial-update/scan budget of 256 MB (256,000,000 bytes) per added subscription.
Reaching the byte budget or interruption before completing the inventory means a
partial scan, with progress retained and remaining work visible.

Accelerate in-use scanning while retaining fresh route admission, preferred-country
priority, separate network pools, stable-route hysteresis and battery-aware idle work.
Choose concrete concurrency/interval changes after measuring the current path.
This request does not require speed-test downloads for every node.
