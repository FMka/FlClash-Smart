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
      expect(settings.smartPreferredCountry, isEmpty);
      expect(settings.smartExcludedCountries, ['RU']);
      expect(settings.smartDnsRecovery, isTrue);
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
                  smartPreferredCountry: 'DE',
                  smartExcludedCountries: ['RU', 'US'],
                  smartDnsRecovery: false,
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
        'preferred-country': 'DE',
        'excluded-countries': ['RU', 'US'],
        'dns-recovery-enabled': false,
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
    'old Russian permission migrates without overriding explicit exclusions',
    () {
      expect(
        AppSettingProps.fromJson({
          'smartAllowRussian': true,
        }).smartExcludedCountries,
        isEmpty,
      );
      expect(
        AppSettingProps.fromJson({
          'smartAllowRussian': false,
        }).smartExcludedCountries,
        ['RU'],
      );
      final restored = AppSettingProps.fromJson({
        'smartAllowRussian': true,
        'smartPreferredCountry': 'DE',
        'smartExcludedCountries': ['RU', 'DE'],
      });
      expect(restored.smartExcludedCountries, ['RU', 'DE']);
      expect(restored.smartPreferredCountry, 'DE');
      expect(restored.toJson().containsKey('smartAllowRussian'), isFalse);
      expect(
        AppSettingProps.fromJson({
          'smartExcludedCountries': [],
        }).smartExcludedCountries,
        isEmpty,
      );
    },
  );

  test('country choices retain absent preferences and exclusions', () {
    const settings = AppSettingProps(
      smartPreferredCountry: 'DE',
      smartExcludedCountries: ['RU', 'US'],
    );
    expect(smartCountryOptions(settings, ['FR']), ['DE', 'FR', 'RU', 'US']);
    expect(smartCountryOptions(settings, []), ['DE', 'RU', 'US']);
    final params = smartReconnectSettings(
      settings.copyWith(
        smartPreferredCountry: ' de ',
        smartExcludedCountries: ['ru', 'RU', 'bad', ' us '],
      ),
    );
    expect(params['preferred-country'], 'DE');
    expect(params['excluded-countries'], ['RU', 'US']);
  });

  test(
    'DNS recovery remains enabled while automatic node selection is off',
    () {
      final container = ProviderContainer();
      addTearDown(container.dispose);
      container.listen(setupPatchProvider, (_, _) {});
      container
          .read(appSettingProvider.notifier)
          .update((settings) => settings.copyWith(smartReconnect: false));
      final manual = container.read(setupPatchProvider);
      expect(
        smartReconnectSettings(container.read(appSettingProvider)),
        containsPair('dns-recovery-enabled', true),
      );
      container
          .read(appSettingProvider.notifier)
          .update((settings) => settings.copyWith(smartDnsRecovery: false));
      expect(container.read(setupPatchProvider), isNot(manual));
    },
  );

  test(
    'country inventory and DNS evidence parse without inventing a provider',
    () {
      final status = SmartStatus.fromJson({
        'discovered-countries': ['DE', 'RU'],
        'unresolved-countries': 7,
        'dns': {
          'enabled': true,
          'phase': 'unavailable',
          'scope': 'direct',
          'source': 'profile',
          'reason': 'resolvers-unavailable',
          'last-checked': 12345,
          'diagnostic-count': 2,
        },
        'routing': {
          'source': 'builtin-ru',
          'version': '2026-10-28',
          'updated-at': '2026-10-28T12:00:00Z',
          'error': 'update-failed',
        },
      });
      expect(status.discoveredCountries, ['DE', 'RU']);
      expect(status.unresolvedCountries, 7);
      expect(status.dns.phase, 'unavailable');
      expect(status.dns.scope, 'direct');
      expect(status.dns.provider, isEmpty);
      expect(status.dns.lastChecked, 12345);
      expect(status.dns.diagnosticCount, 2);
      expect(status.routing.source, 'builtin-ru');
      expect(status.routing.version, '2026-10-28');
      expect(status.routing.error, 'update-failed');
      expect(SmartStatus.fromJson({}).dns.phase, 'idle');
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
