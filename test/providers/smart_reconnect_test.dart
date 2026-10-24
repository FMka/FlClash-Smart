import 'dart:async';
import 'dart:convert';

import 'package:fl_clash/common/smart_reconnect.dart';
import 'package:fl_clash/core/controller.dart';
import 'package:fl_clash/core/interface.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/providers/service_status.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:mocktail/mocktail.dart';

class _Core extends Mock implements CoreHandlerInterface {}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('new and old settings enable smart selection without a script', () {
    for (final settings in [
      const AppSettingProps(),
      AppSettingProps.fromJson({}),
      AppSettingProps.safeFromJson({
        'serviceOrder': [],
        'disabledServices': [],
      }),
    ]) {
      expect(settings.smartReconnect, isTrue);
      expect(settings.smartAllowRussian, isFalse);
      expect(settings.smartMobileBudgetMiB, 20);
      expect(settings.smartWifiBudgetMiB, 100);
      expect(smartServiceIds(settings), [
        'youtube',
        'google',
        'chatgpt',
        'telegram',
      ]);
    }
  });

  test(
    'persisted choices survive restore and override legacy script settings',
    () {
      final settings = AppSettingProps.fromJson(
        jsonDecode(
              jsonEncode(
                const AppSettingProps(
                  smartReconnect: false,
                  smartAllowRussian: true,
                  smartMobileBudgetMiB: 5,
                  smartWifiBudgetMiB: 40,
                ).toJson(),
              ),
            )
            as Map<String, dynamic>,
      );
      final oldConfig = <String, dynamic>{
        'smart-reconnect-settings': {'enabled': true},
        'proxy-groups': [
          {'name': 'Another provider', 'smart-reconnect': true},
        ],
        'rules': ['MATCH,Another provider'],
      };
      final config = withSmartReconnectSettings(oldConfig, settings);
      expect(config['smart-reconnect-settings'], {
        'enabled': false,
        'allow-russian': true,
        'services': ['youtube', 'google', 'chatgpt', 'telegram'],
        'mobile-budget-mib': 5,
        'wifi-budget-mib': 40,
      });
      expect(config['rules'], same(oldConfig['rules']));
      expect(config['proxy-groups'], same(oldConfig['proxy-groups']));
      expect(oldConfig['smart-reconnect-settings'], {'enabled': true});
    },
  );

  test(
    'dashboard service choice is exactly the smart choice and triggers apply',
    () {
      final container = ProviderContainer();
      addTearDown(container.dispose);
      container.listen(setupPatchProvider, (_, _) {});
      final initial = container.read(setupPatchProvider);
      container
          .read(appSettingProvider.notifier)
          .update(
            (settings) => settings.copyWith(
              serviceOrder: ['telegram', 'google'],
              disabledServices: [
                'youtube',
                'chatgpt',
                'github',
                'claude',
                'gemini',
                'netflix',
                'disney-plus',
                'prime-video',
                'spotify',
                'tiktok',
                'discord',
                'bilibili',
              ],
            ),
          );
      final settings = container.read(appSettingProvider);
      expect(
        smartServiceIds(settings),
        container
            .read(enabledServiceTargetsProvider)
            .map((target) => target.id)
            .toList(),
      );
      expect(smartServiceIds(settings), ['telegram', 'google']);
      expect(container.read(setupPatchProvider), isNot(initial));
      final selected = container.read(setupPatchProvider);
      container
          .read(appSettingProvider.notifier)
          .update((settings) => settings.copyWith(smartReconnect: false));
      expect(container.read(setupPatchProvider), isNot(selected));
    },
  );

  test(
    'hide only service REJECT in managed groups and expose manual state',
    () {
      const reject = Proxy(name: 'REJECT', type: 'Reject');
      const node = Proxy(name: 'Working', type: 'Vless');
      final groups = [
        const Group(
          name: 'Main',
          type: GroupType.Selector,
          all: [reject, node],
        ),
        const Group(
          name: 'Other',
          type: GroupType.Selector,
          all: [reject, node],
        ),
      ];
      final status = SmartStatus.fromJson({
        'enabled': true,
        'phase': 'manual',
        'groups': [
          {'name': 'Main', 'mode': 'manual', 'current': 'Working'},
        ],
      });
      final shown = withoutSmartReject(groups, status);
      expect(shown.first.all.map((node) => node.name), ['Working']);
      expect(shown.last.all, [reject, node]);
      expect(status.manual, isTrue);
      expect(status.selections, {'Main': 'Working'});
    },
  );

  test('late status response cannot restore a selection after Stop', () async {
    final core = _Core();
    final gate = Completer<Map<String, dynamic>>();
    when(core.getSmartStatus).thenAnswer((_) => gate.future);
    final container = ProviderContainer(
      overrides: [
        coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
      ],
    );
    addTearDown(container.dispose);
    final notifier = container.read(smartStatusProvider.notifier);
    final pending = notifier.refresh();
    notifier.clear();
    gate.complete({
      'enabled': true,
      'phase': 'connected',
      'groups': [
        {'name': 'Main', 'current': 'Old'},
      ],
    });
    await pending;
    expect(container.read(smartStatusProvider).selections, isEmpty);
    expect(container.read(smartStatusProvider).phase, 'stopped');
  });
}
