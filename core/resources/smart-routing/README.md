# Bundled Russian bypass domains

Snapshot downloaded on 2026-10-28 from:
https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/meta/geo/geosite/category-ru.list

Original snapshot SHA-256:
`272b2533badf77a9bad73f3a9d0eb2290bd2d5f04a53b40c25fd8cf2b121f4b4`

MetaCubeX/meta-rules-dat distributes the compiled domain categories under GPL-3.0.
Upstream category data includes v2fly/domain-list-community, licensed under MIT.
Both license notices are retained alongside this snapshot and embedded with it.
The original domain-list files and build process remain available at:

- https://github.com/MetaCubeX/meta-rules-dat
- https://github.com/v2fly/domain-list-community

The app validates a cached or downloaded list before using it, preserves the last
valid copy on failure and uses this bundled snapshot for first-run offline setup.
This preset applies only when the profile supplies no routing policy. It contains
domestic domain categories; it is independent of the VPN-exit country filter.
