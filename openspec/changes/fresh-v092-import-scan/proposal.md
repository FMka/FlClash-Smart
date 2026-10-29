# Fresh FlClash 0.9.2 with Smart and subscription discovery

Approved implementation task, 2026-10-29. Replace the active source line with
official latest stable v0.9.2 (`7fd417fb`), retaining its mihomo submodule and
platform ownership. Preserve the old Smart V3.1 history on an archive branch;
do not diagnose or repair the rejected old APK.

Port all agreed Smart functionality through V3.1, excluding V4/V5 free servers.
Remove automatic application update checks/prompts. Immediately after first-run
agreement acceptance, offer importing a subscription using native import controls.

Every successfully imported subscription starts a bounded cancellable discovery
pass, including when it is not the active profile or VPN is stopped. Discovery
updates actual exit countries and the current transport's complete response pool,
without switching profiles, starting VPN, selecting a route or running speed tests.
Expose progress and partial/unsupported outcomes honestly. Existing battery, traffic,
country admission, manual selection and Stop constraints remain.
