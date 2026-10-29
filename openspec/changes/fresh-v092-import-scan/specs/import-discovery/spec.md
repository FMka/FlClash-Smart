## ADDED Requirements

### Requirement: First subscription onboarding
After accepting the first-run agreement the app SHALL offer adding a subscription,
using the existing link, QR and file controls and allowing deferral.

#### Scenario: First agreement accepted
- WHEN consent is accepted and no personal subscription exists
- THEN the import offer appears without typing a statement or an app-update prompt
- AND deferral reaches the app without starting VPN.

### Requirement: Isolated import discovery
Every newly imported subscription SHALL receive a bounded scan of response and
actual exit-country evidence without changing the active profile or VPN state.

#### Scenario: Import while another subscription is selected
- WHEN the new subscription is saved successfully
- THEN its isolated scan starts and the previous selection remains unchanged
- AND discovered replies update only the current transport pool with matching identities.

#### Scenario: Cancellation and limits
- WHEN a scan is stopped, superseded, loses its network, or reaches its time/byte limit
- THEN no obsolete result changes routing or is reported as a fully successful scan
- AND saved subscriptions remain usable with honest partial/cancelled status.
