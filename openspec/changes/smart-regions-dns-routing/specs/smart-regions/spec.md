## ADDED Requirements

### Requirement: Country-restricted automatic selection

The app SHALL offer a single preferred-country selector defaulting to Any and an
independent excluded-country list defaulting to Russia (RU). Any SHALL mean all
verified exit countries except the exclusions. An excluded country SHALL never be
automatically selected, even when it is also the preferred country. Both settings
SHALL persist across restarts and subscription refreshes. Exclusions SHALL remain
stored even when a subscription currently has no servers in those countries.

#### Scenario: Default settings are used

- WHEN no country settings have been changed
- THEN Smart SHALL consider all otherwise qualified exits except Russia.

#### Scenario: A preferred country is selected

- WHEN a user selects a country
- THEN Smart SHALL admit only verified exits in that country and still enforce
  service availability and the excluded-country list.

#### Scenario: A preferred country is also excluded

- WHEN restored settings contain a preferred country present in the exclusion list
- THEN Smart SHALL report the conflict and SHALL NOT select that country or
  silently widen the preference to Any.

#### Scenario: No eligible node remains

- WHEN the preferred country has no currently qualified non-excluded server
- THEN the app SHALL report that state without selecting another country or DIRECT.

### Requirement: Discover available countries after subscription changes

The app SHALL discover exit countries through bounded node-pinned probes after
subscription addition or relevant node changes. Server names and ingress IPs SHALL
not be presented as verified exit countries. Unknown results SHALL remain explicit.

#### Scenario: A new subscription is added

- WHEN eligible nodes are imported
- THEN country discovery SHALL progressively populate the country selector, reuse
  valid identity-aware cache entries and support cancellation without starting VPN.
