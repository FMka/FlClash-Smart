## MODIFIED Requirements

### Requirement: Complete transport pools
The app SHALL persist all previously responding current-subscription nodes in
separate Wi-Fi and mobile lists, with latest check/failure evidence, without a
fixed successful-node count or silent ageing out. Refresh work SHALL remain bounded.

#### Scenario: More than 128 working nodes and process restart
- WHEN more than 128 nodes respond and history is restored after a restart
- THEN all matching saved nodes remain available for scheduling and display
- AND speed timestamps are preserved and freshly qualified selection is still required.

#### Scenario: Network handoff with a common saved current node
- WHEN switching Wi-Fi/mobile and the unchanged current node is saved in both pools
- THEN the selector retains that node while its current health is checked
- AND a failed current check triggers replacement without overriding Stop.

### Requirement: Preferred-country priority with fallback
Automatic selection SHALL try the preferred country first, fall back to other
non-excluded countries when necessary, and return after a preferred node qualifies.

#### Scenario: Preferred outage and recovery
- WHEN preferred nodes fail and a different-country node qualifies
- THEN a low-latency qualified fallback can be used
- AND a subsequently qualified preferred node takes priority over hold/speed rules.

## REMOVED Requirements

### Requirement: Automatic application update check
The app SHALL NOT check application releases or open update prompts automatically,
including after a clean install and with old saved automatic-update settings.

#### Scenario: Startup with a legacy enabled preference
- WHEN the app starts with an old auto-check flag set
- THEN no release request or update dialog is started without an explicit user action.
