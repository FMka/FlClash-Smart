# Tasks

- [x] Record two country controls: preferred Any, exclusions Russia by default.
- [x] Confirm subscription rules take precedence; fallback preset only when absent.
- [x] Apply the correction: do not add Sberbank or VK to priority checks.
- [x] Inspect existing DNS/rule integration and verify public resolver/rule sources.
- [x] Inspect the referenced VK incident attachments and later supplied chat text;
  record confirmed DNS timeouts, the unverified proposed fix and observer limits.
- [x] Record battery priority: passive signals, shared bounded recovery, no new
  DNS polling/wakeup loop and deferred optional work during inactivity.
- [x] Finish discussion of proposed resolver defaults and rule provenance.
- [x] Implement country settings, bounded discovery, migration and admission.
- [x] Implement subscription-routing fallback with validated cached rule data.
- [x] Implement bounded DNS diagnosis/recovery and status without conflicting owners.
- [x] Automate bypass-name DNS failures, blocked UDP-53 bootstrap, preserved
  private/filtering policies and UI-detached recovery; real-carrier replay is pending.
- [x] Generate models/localizations and run covering automated checks.
- [x] Build and verify the Android ARM64 APK for direct delivery without an archive.
- [ ] Validate Wi-Fi/LTE, sleep and real services on the user's phone.
- [ ] Verify idle produces no new DNS probes, real screen-off failures still recover,
  and compare diagnostic activity/battery on the phone before claiming savings.
