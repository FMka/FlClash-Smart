## ADDED Requirements

### Requirement: Prefer subscription routing rules

Rule mode SHALL be the default. Supplied subscription rules and their order SHALL
be preserved unless the user explicitly overrides them. The built-in Russian bypass
preset SHALL be used only when subscription routing rules are absent. Personal
exceptions SHALL keep their existing precedence.

#### Scenario: Subscription rules are supplied

- WHEN a profile includes valid routing rules
- THEN the profile SHALL use those rules without silently merging the Russian preset.

#### Scenario: A subscription supplies only servers

- WHEN no subscription routing policy is supplied
- THEN a bundled Russian bypass preset SHALL route its domestic-domain categories
  directly and the remaining traffic through the managed VPN route.

### Requirement: Preserve usable rule data

Externally updated rule data SHALL be validated before activation. The last usable
copy and source/version SHALL remain available if an update fails.

#### Scenario: Domestic bypass services are present

- WHEN Sberbank or VK are included in the applicable bypass policy
- THEN their direct reachability SHALL NOT be treated as VPN health, and neither
  SHALL be added to the default priority-service checks.
