# Smart regions, DNS recovery and routing sources

Date: 2026-10-28. Status: implemented; automated verification and Android test build
recorded in `SMART_VERIFICATION.md`; device acceptance pending.

## Why

The user requested country restrictions for Smart selection, DNS-provider recovery,
and a clear default source of bypass rules. This draft supersedes the earlier
proposal to add Sberbank and VK to the dashboard checks: neither is added.

## Agreed scope

- Provide exactly two country controls: a single preferred country, default Any,
  and an excluded-country list, default Russia (RU). These replace both the existing
  Russian permission switch and the earlier proposed Russian exclusion switch.
- Any means all verified countries except the exclusions. A specific preferred
  country strictly restricts automatic candidates; exclusions always take precedence.
  Both settings persist independently across restarts and subscription changes.
- Use subscription routing rules when supplied; use the built-in Russian bypass
  preset only when subscription rules are absent. Preserve personal exceptions.
- Keep YouTube, Google, ChatGPT and Telegram as the default priority checks.
- Add bounded automatic DNS recovery and a curated built-in public resolver list.
- Enable DNS recovery by default, with a persistent opt-out and no script setup;
  include real public-name failures affecting bypass as well as VPN traffic.
- Prioritize battery life across Smart features: reuse real traffic observations,
  share bounded diagnostics and defer optional work during inactivity. DNS recovery
  SHALL NOT introduce periodic polling or additional device-wakeup mechanisms.

## Approved implementation details

- No qualifying server means an explicit unavailable state, without silently
  widening the preferred country or clearing an excluded-country selection.
- Try the existing profile DNS first; select alternatives only after confirmed DNS
  failure. Keep direct bootstrap, node resolution and tunneled destination DNS distinct.
- Use passive Core resolver events without debug logging or an open app screen;
  preserve routing, local/private DNS and intentional filtering policies.
- Use unfiltered public services in the default pool; filtering variants remain an
  explicit separate choice. Availability in the user's carrier network is unverified.
- Distinguish real subscription rules from app-generated catch-all rules using
  provenance before adding personal rules. An explicitly imported MATCH is a
  supplied policy and is preserved; it does not claim complete bypass coverage.

## Impact and boundaries

Settings, subscription ingestion, Smart admission/status, DNS orchestration and
profile assembly are affected. Existing lifecycle ownership, latest-intent safety,
per-network history and diagnostic traffic accounting must remain intact.
No provider account, private subscription, host network or remote repository setting
was changed during planning. OpenSpec CLI installation/validation was not performed.
