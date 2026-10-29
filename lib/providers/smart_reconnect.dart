import 'package:fl_clash/models/models.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'core.dart';

class SmartRoutingStatus {
  const SmartRoutingStatus({
    this.source = 'none',
    this.version = '',
    this.updatedAt = '',
    this.error = '',
  });

  factory SmartRoutingStatus.fromJson(Map<String, dynamic> json) =>
      SmartRoutingStatus(
        source: json['source'] as String? ?? 'none',
        version: json['version'] as String? ?? '',
        updatedAt: json['updated-at'] as String? ?? '',
        error: json['error'] as String? ?? '',
      );

  final String source;
  final String version;
  final String updatedAt;
  final String error;
}

class SmartDnsStatus {
  const SmartDnsStatus({
    this.enabled = true,
    this.phase = 'idle',
    this.scope = '',
    this.source = 'profile',
    this.provider = '',
    this.endpoint = '',
    this.reason = '',
    this.lastChecked = 0,
    this.diagnosticCount = 0,
  });

  factory SmartDnsStatus.fromJson(Map<String, dynamic> json) => SmartDnsStatus(
    enabled: json['enabled'] as bool? ?? true,
    phase: json['phase'] as String? ?? 'idle',
    scope: json['scope'] as String? ?? '',
    source: json['source'] as String? ?? 'profile',
    provider: json['provider'] as String? ?? '',
    endpoint: json['endpoint'] as String? ?? '',
    reason: json['reason'] as String? ?? '',
    lastChecked: (json['last-checked'] as num?)?.toInt() ?? 0,
    diagnosticCount: (json['diagnostic-count'] as num?)?.toInt() ?? 0,
  );

  final bool enabled;
  final String phase;
  final String scope;
  final String source;
  final String provider;
  final String endpoint;
  final String reason;
  final int lastChecked;
  final int diagnosticCount;
}

class SmartGroupStatus {
  const SmartGroupStatus({
    required this.name,
    required this.mode,
    required this.current,
    required this.phase,
    required this.country,
    required this.lastChecked,
    required this.lastSpeed,
    this.speedAt = 0,
    this.speedSource = '',
    this.pool = const [],
    this.wifiPool = const [],
    this.mobilePool = const [],
    this.poolCount = 0,
    this.wifiPoolCount = 0,
    this.mobilePoolCount = 0,
    this.reason = '',
  });

  factory SmartGroupStatus.fromJson(Map<String, dynamic> json) =>
      SmartGroupStatus(
        name: json['name'] as String? ?? '',
        mode: json['mode'] as String? ?? 'auto',
        current: json['current'] as String? ?? '',
        phase: json['phase'] as String? ?? '',
        country: json['country'] as String? ?? '',
        lastChecked: json['last-checked']?.toString() ?? '',
        speedAt: (json['speed-at'] as num?)?.toInt() ?? 0,
        speedSource: json['speed-source'] as String? ?? '',
        pool: [
          for (final entry in json['pool'] as List? ?? const [])
            if (entry is Map<String, dynamic>) entry,
        ],
        wifiPool: [
          for (final entry in json['wifi-pool'] as List? ?? const [])
            if (entry is Map<String, dynamic>) entry,
        ],
        mobilePool: [
          for (final entry in json['mobile-pool'] as List? ?? const [])
            if (entry is Map<String, dynamic>) entry,
        ],
        poolCount:
            (json['pool-count'] as num?)?.toInt() ??
            (json['pool'] as List?)?.length ??
            0,
        wifiPoolCount:
            (json['wifi-pool-count'] as num?)?.toInt() ??
            (json['wifi-pool'] as List?)?.length ??
            0,
        mobilePoolCount:
            (json['mobile-pool-count'] as num?)?.toInt() ??
            (json['mobile-pool'] as List?)?.length ??
            0,
        reason: json['reason'] as String? ?? '',
        lastSpeed:
            (json['last-speed-bytes-per-second'] as num?)?.toDouble() ?? 0,
      );

  final String name;
  final String mode;
  final String current;
  final String phase;
  final String country;
  final String lastChecked;
  final double lastSpeed;
  final int speedAt;
  final String speedSource;
  final List<Map<String, dynamic>> pool;
  final List<Map<String, dynamic>> wifiPool;
  final List<Map<String, dynamic>> mobilePool;
  final int poolCount;
  final int wifiPoolCount;
  final int mobilePoolCount;
  final String reason;
}

class SmartStatus {
  const SmartStatus({
    this.enabled = false,
    this.phase = 'stopped',
    this.network = '',
    this.groups = const [],
    this.diagnosticUsed = 0,
    this.mobileUsed = 0,
    this.wifiUsed = 0,
    this.mobileBudget = 20 * 1024 * 1024,
    this.wifiBudget = 100 * 1024 * 1024,
    this.discoveredCountries = const [],
    this.unresolvedCountries = 0,
    this.dns = const SmartDnsStatus(),
    this.routing = const SmartRoutingStatus(),
  });

  factory SmartStatus.fromJson(Map<String, dynamic> json) => SmartStatus(
    enabled: json['enabled'] as bool? ?? false,
    phase: json['phase'] as String? ?? '',
    network: json['network'] as String? ?? '',
    groups: [
      for (final group in json['groups'] as List? ?? const [])
        if (group is Map<String, dynamic>) SmartGroupStatus.fromJson(group),
    ],
    diagnosticUsed: (json['diagnostic-used-bytes'] as num?)?.toInt() ?? 0,
    mobileUsed: (json['mobile-used-bytes'] as num?)?.toInt() ?? 0,
    wifiUsed: (json['wifi-used-bytes'] as num?)?.toInt() ?? 0,
    mobileBudget: (json['mobile-budget-bytes'] as num?)?.toInt() ?? 0,
    wifiBudget: (json['wifi-budget-bytes'] as num?)?.toInt() ?? 0,
    discoveredCountries: [
      for (final country in json['discovered-countries'] as List? ?? const [])
        if (country is String) country,
    ],
    unresolvedCountries: (json['unresolved-countries'] as num?)?.toInt() ?? 0,
    dns: json['dns'] is Map<String, dynamic>
        ? SmartDnsStatus.fromJson(json['dns'] as Map<String, dynamic>)
        : const SmartDnsStatus(),
    routing: json['routing'] is Map<String, dynamic>
        ? SmartRoutingStatus.fromJson(json['routing'] as Map<String, dynamic>)
        : const SmartRoutingStatus(),
  );

  final bool enabled;
  final String phase;
  final String network;
  final List<SmartGroupStatus> groups;
  final int diagnosticUsed;
  final int mobileUsed;
  final int wifiUsed;
  final int mobileBudget;
  final int wifiBudget;
  final List<String> discoveredCountries;
  final int unresolvedCountries;
  final SmartDnsStatus dns;
  final SmartRoutingStatus routing;

  Set<String> get managedGroups => groups.map((group) => group.name).toSet();
  Map<String, String> get selections => {
    for (final group in groups)
      if (group.current.isNotEmpty) group.name: group.current,
  };
  bool get manual => groups.any((group) => group.mode == 'manual');
}

final smartStatusProvider = NotifierProvider<SmartStatusNotifier, SmartStatus>(
  SmartStatusNotifier.new,
);

final smartPoolSnapshotProvider = FutureProvider.autoDispose<SmartStatus>((
  ref,
) async {
  return SmartStatus.fromJson(
    await ref.read(coreHandlerProvider).getSmartStatus(includePools: true),
  );
});

class SmartStatusNotifier extends Notifier<SmartStatus> {
  bool _reading = false;
  int _generation = 0;

  @override
  SmartStatus build() => const SmartStatus();

  void clear() {
    _generation++;
    state = SmartStatus(
      diagnosticUsed: state.diagnosticUsed,
      mobileUsed: state.mobileUsed,
      wifiUsed: state.wifiUsed,
      mobileBudget: state.mobileBudget,
      wifiBudget: state.wifiBudget,
    );
  }

  Future<void> refresh() async {
    if (_reading) return;
    _reading = true;
    final generation = _generation;
    try {
      final value = await ref.read(coreHandlerProvider).getSmartStatus();
      if (ref.mounted && generation == _generation) {
        state = SmartStatus.fromJson(value);
      }
    } finally {
      _reading = false;
    }
  }
}

List<Group> withoutSmartReject(List<Group> groups, SmartStatus status) => [
  for (final group in groups)
    status.managedGroups.contains(group.name)
        ? group.copyWith(
            all: group.all.where((proxy) => proxy.name != 'REJECT').toList(),
          )
        : group,
];
