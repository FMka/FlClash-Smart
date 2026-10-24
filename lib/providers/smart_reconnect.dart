import 'package:fl_clash/models/models.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'core.dart';

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
