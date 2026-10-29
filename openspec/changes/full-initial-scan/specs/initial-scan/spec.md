## MODIFIED Requirements

### Requirement: Complete initial discovery with an explicit traffic budget
Newly added subscriptions SHALL refresh their complete effective proxy inventory,
including associated proxy providers, and attempt the complete node list. The shared
first-update/discovery budget SHALL be 256,000,000 bytes per added subscription.
It SHALL account for diagnostic request/response payload, inventory refreshes,
retries and partial or cancelled transfers, separately from user traffic.

#### Scenario: A list exceeds the old initial node or duration limits
- WHEN the scan has remaining nodes and available traffic budget
- THEN the old 64/256-node and 24/45-second caps do not mark the scan complete
- AND node timeouts, cancellation and user Stop remain effective.

#### Scenario: Budget or interruption prevents completion
- WHEN the budget is exhausted or the scan is interrupted before the list is covered
- THEN the app reports partial/cancelled progress and the remaining node count
- AND retained progress can be continued without mixing Wi-Fi and mobile evidence.

### Requirement: Faster working discovery with stable routing
In-use scanning SHALL be accelerated using measured scheduling/concurrency changes,
while retaining battery-aware idle behavior, fresh candidate qualification and the
existing protections against unnecessary healthy-route switches.
