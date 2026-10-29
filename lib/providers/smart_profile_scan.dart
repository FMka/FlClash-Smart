import 'dart:async';
import 'dart:collection';

import 'package:fl_clash/core/controller.dart';
import 'package:fl_clash/core/method.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/models.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'action.dart';
import 'app.dart';
import 'config.dart';
import 'core.dart';
import 'database.dart';
import 'smart_reconnect.dart';
import 'state.dart';

class SmartProfileScanState {
  const SmartProfileScanState({
    this.profileId,
    this.label = '',
    this.phase = 'idle',
    this.checked = 0,
    this.total = 0,
    this.responded = 0,
    this.unsupported = 0,
    this.countries = const [],
    this.queued = 0,
  });

  final int? profileId;
  final String label;
  final String phase;
  final int checked;
  final int total;
  final int responded;
  final int unsupported;
  final List<String> countries;
  final int queued;

  bool get running => phase == 'preparing' || phase == 'scanning';
}

final smartProfileScanProvider =
    NotifierProvider<SmartProfileScanNotifier, SmartProfileScanState>(
      SmartProfileScanNotifier.new,
    );

class SmartProfileScanNotifier extends Notifier<SmartProfileScanState> {
  final _pending = Queue<int>();
  var _draining = false;
  var _generation = 0;
  String? _scanId;
  int? _scanProfileId;
  late CoreController _core;

  @override
  SmartProfileScanState build() {
    _core = ref.read(coreHandlerProvider);
    ref.onDispose(() {
      _generation++;
      _pending.clear();
      final scanId = _scanId;
      final profileId = _scanProfileId;
      if (scanId != null && profileId != null) {
        unawaited(_cancelNative(scanId, profileId));
      }
    });
    ref.listen(initProvider, (_, ready) {
      if (ready) _schedule();
    });
    ref.listen(appVisibleProvider, (_, visible) {
      if (visible) {
        _schedule();
      } else {
        unawaited(cancel());
      }
    });
    ref.listen(isStartProvider, (previous, running) {
      if (previous == true && !running) unawaited(cancel());
    });
    ref.listen(coreStatusProvider, (previous, next) {
      if (previous == CoreStatus.connected && next != CoreStatus.connected) {
        unawaited(cancel());
      }
    });
    ref.listen(profilesProvider, (_, profiles) {
      _pending.removeWhere((id) => profiles.getProfile(id) == null);
      if (state.running && profiles.getProfile(state.profileId) == null) {
        unawaited(cancel(clearQueue: false));
      }
    });
    return const SmartProfileScanState();
  }

  void request(int profileId) {
    if (_pending.contains(profileId) ||
        (state.running && state.profileId == profileId)) {
      return;
    }
    _pending.add(profileId);
    _schedule();
  }

  void _schedule() {
    if (_draining ||
        !ref.read(initProvider) ||
        !ref.read(appVisibleProvider) ||
        _pending.isEmpty) {
      return;
    }
    unawaited(_drain());
  }

  Future<void> cancel({bool clearQueue = true}) async {
    _generation++;
    if (clearQueue) _pending.clear();
    final scanId = _scanId;
    final profileId = _scanProfileId;
    _scanId = null;
    _scanProfileId = null;
    if (state.running) {
      state = SmartProfileScanState(
        profileId: state.profileId,
        label: state.label,
        phase: 'cancelled',
        checked: state.checked,
        total: state.total,
        responded: state.responded,
        unsupported: state.unsupported,
        countries: state.countries,
        queued: _pending.length,
      );
    }
    if (scanId != null && profileId != null) {
      await _cancelNative(scanId, profileId);
    }
  }

  Future<void> _cancelNative(String scanId, int profileId) async {
    try {
      await _core.scanSmartProfile({
        'operation': 'cancel',
        'scan-id': scanId,
        'profile-id': '$profileId',
      });
    } catch (_) {}
  }

  void dismiss() {
    if (!state.running) state = const SmartProfileScanState();
  }

  bool _isCurrent(int generation) => ref.mounted && generation == _generation;

  Future<Map<String, dynamic>> _startScan(
    Map<String, dynamic> params,
    int generation,
  ) async {
    for (var attempt = 0; attempt < 3; attempt++) {
      if (!_isCurrent(generation)) return const {};
      try {
        final result = await _core.scanSmartProfile(params);
        final noNetwork =
            (result['scan-id'] as String? ?? '').isEmpty &&
            result['reason'] == 'network-unavailable';
        if (!noNetwork || attempt == 2) return result;
      } on CoreMethodException catch (error) {
        if (attempt == 2 ||
            (error.code != 'network_unavailable' &&
                error.message != 'network unavailable')) {
          rethrow;
        }
      }
      await Future<void>.delayed(const Duration(seconds: 1));
    }
    return const {};
  }

  Future<void> _drain() async {
    _draining = true;
    try {
      while (ref.mounted && _pending.isNotEmpty) {
        final profileId = _pending.removeFirst();
        final profile = ref.read(profilesProvider).getProfile(profileId);
        if (profile == null) continue;
        final generation = ++_generation;
        state = SmartProfileScanState(
          profileId: profileId,
          label: profile.realLabel,
          phase: 'preparing',
          queued: _pending.length,
        );
        try {
          final setup = await ref.read(setupStateProvider(profileId).future);
          final built = await ref
              .read(setupActionProvider.notifier)
              .getProfile(
                setupState: setup,
                patchConfig: ref.read(patchClashConfigProvider),
              );
          if (!_isCurrent(generation)) continue;
          final settings = ref.read(appSettingProvider);
          final core = ref.read(coreHandlerProvider);
          var result = await _startScan({
            'operation': 'start',
            'profile-id': '$profileId',
            'params': SetupParams(
              selectedMap: const {},
              testUrl: ref.read(patchClashConfigProvider).testUrl,
              skipCertVerify: !settings.checkCertificate,
              base: built.base,
            ).toJson(),
            if (built.base == null) 'config': built.yaml,
          }, generation);
          final scanId = result['scan-id'] as String?;
          if (!_isCurrent(generation)) {
            if (scanId != null) await _cancelNative(scanId, profileId);
            continue;
          }
          _scanId = scanId;
          _scanProfileId = profileId;
          if (scanId == null && result['phase'] == 'scanning') {
            throw const FormatException('Missing scan identity');
          }
          _publish(result);
          final deadline = DateTime.now().add(const Duration(seconds: 60));
          while (_isCurrent(generation) &&
              state.phase == 'scanning' &&
              DateTime.now().isBefore(deadline)) {
            await Future<void>.delayed(const Duration(seconds: 5));
            if (!_isCurrent(generation)) break;
            result = await core.scanSmartProfile({
              'operation': 'status',
              'scan-id': scanId,
              'profile-id': '$profileId',
            });
            if (_isCurrent(generation)) _publish(result);
          }
          if (_isCurrent(generation)) {
            if (state.phase == 'scanning' && scanId != null) {
              await _cancelNative(scanId, profileId);
              if (!_isCurrent(generation)) continue;
              _publish({...result, 'phase': 'partial'});
            }
            _scanId = null;
            _scanProfileId = null;
            ref.invalidate(smartPoolSnapshotProvider);
            try {
              await ref.read(smartStatusProvider.notifier).refresh();
            } catch (_) {}
          }
        } catch (_) {
          if (_isCurrent(generation)) {
            state = SmartProfileScanState(
              profileId: profileId,
              label: profile.realLabel,
              phase: 'failed',
              queued: _pending.length,
            );
          }
        } finally {
          if (_isCurrent(generation)) {
            final scanId = _scanId;
            _scanId = null;
            _scanProfileId = null;
            if (scanId != null) await _cancelNative(scanId, profileId);
          }
        }
      }
    } finally {
      _draining = false;
      if (ref.mounted) _schedule();
    }
  }

  void _publish(Map<String, dynamic> result) {
    state = SmartProfileScanState(
      profileId: state.profileId,
      label: state.label,
      phase: result['phase'] as String? ?? 'failed',
      checked: (result['checked'] as num?)?.toInt() ?? 0,
      total: (result['total'] as num?)?.toInt() ?? 0,
      responded: (result['responded'] as num?)?.toInt() ?? 0,
      unsupported: (result['unsupported'] as num?)?.toInt() ?? 0,
      countries: [
        for (final country in result['countries'] as List? ?? const [])
          if (country is String) country,
      ],
      queued: _pending.length,
    );
  }
}
