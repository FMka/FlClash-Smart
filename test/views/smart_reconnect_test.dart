import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/ui/list.dart';
import 'package:fl_clash/views/config/smart_reconnect.dart';
import 'package:fl_clash/views/disclaimer.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';

import '../helpers/test_app.dart';

class _Status extends SmartStatusNotifier {
  @override
  SmartStatus build() => SmartStatus.fromJson({
    'enabled': true,
    'phase': 'connected',
    'discovered-countries': ['DE', 'FR'],
    'unresolved-countries': 3,
    'dns': {
      'phase': 'recovered',
      'source': 'recovery',
      'provider': 'Cloudflare',
      'scope': 'direct',
      'reason': 'recovered',
    },
    'routing': {'source': 'subscription'},
    'groups': [
      {
        'name': 'Any subscription',
        'mode': 'auto',
        'current': 'Actual server',
        'country': 'DE',
        'wifi-pool': List.generate(
          300,
          (index) => {
            'name': 'Wi-Fi server $index',
            'country': 'DE',
            'latency-ms': index + 1,
            'checked': 1,
            'available': true,
          },
        ),
        'mobile-pool': List.generate(
          200,
          (index) => {
            'name': 'LTE server $index',
            'country': 'FR',
            'latency-ms': index + 2,
            'checked': 1,
            'available': true,
          },
        ),
      },
    ],
  });
}

void main() {
  Future<ProviderContainer> openSmartSettings(WidgetTester tester) async {
    await tester.binding.setSurfaceSize(const Size(900, 1800));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final container = ProviderContainer(
      overrides: [
        isStartProvider.overrideWithValue(true),
        smartStatusProvider.overrideWith(_Status.new),
      ],
    );
    addTearDown(container.dispose);
    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const TestApp(locale: Locale('en'), child: SmartReconnectView()),
      ),
    );
    await tester.pumpAndSettle();
    return container;
  }

  testWidgets('shows country priority, passive DNS state and rule source', (
    tester,
  ) async {
    final container = await openSmartSettings(tester);
    expect(find.text('Preferred country'), findsOneWidget);
    expect(find.text('Excluded countries'), findsOneWidget);
    expect(find.text('Any'), findsOneWidget);
    expect(find.text('🇷🇺 RU'), findsOneWidget);
    expect(find.textContaining('2 countries verified'), findsOneWidget);
    expect(find.textContaining('3 servers unresolved'), findsOneWidget);
    expect(find.textContaining('Subscription rules'), findsOneWidget);
    expect(find.textContaining('Cloudflare'), findsOneWidget);
    expect(find.textContaining('Bypass DNS'), findsOneWidget);
    expect(
      find.text('Include Russian servers in automatic selection'),
      findsNothing,
    );

    container
        .read(appSettingProvider.notifier)
        .update((settings) => settings.copyWith(smartPreferredCountry: 'RU'));
    await tester.pumpAndSettle();
    expect(
      find.textContaining('The preferred country is excluded.'),
      findsOneWidget,
    );
    expect(container.read(appSettingProvider).smartPreferredCountry, 'RU');
    expect(container.read(appSettingProvider).smartExcludedCountries, ['RU']);
  });

  for (final mobile in [false, true]) {
    testWidgets('complete ${mobile ? 'LTE' : 'Wi-Fi'} pool renders lazily', (
      tester,
    ) async {
      final container = ProviderContainer(
        overrides: [
          smartStatusProvider.overrideWith(_Status.new),
          smartPoolSnapshotProvider.overrideWith(
            (ref) async => ref.read(smartStatusProvider),
          ),
        ],
      );
      addTearDown(container.dispose);
      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: TestApp(
            locale: const Locale('en'),
            child: SmartPoolView(groupName: 'Any subscription', mobile: mobile),
          ),
        ),
      );
      await tester.pumpAndSettle();
      final group = container.read(smartStatusProvider).groups.single;
      expect(group.wifiPool, hasLength(300));
      expect(group.mobilePool, hasLength(200));
      expect(find.text('${mobile ? 'LTE' : 'Wi-Fi'} server 0'), findsOneWidget);
      expect(find.text('${mobile ? 'Wi-Fi' : 'LTE'} server 0'), findsNothing);
      expect(
        find.text('${mobile ? 'LTE' : 'Wi-Fi'} server ${mobile ? 199 : 299}'),
        findsNothing,
      );
    });
  }

  testWidgets('country exclusions remain editable when the exit is absent', (
    tester,
  ) async {
    final container = await openSmartSettings(tester);
    await tester.tap(find.text('Excluded countries'));
    await tester.pumpAndSettle();
    expect(find.text('🇩🇪 DE'), findsOneWidget);
    expect(find.text('🇫🇷 FR'), findsOneWidget);
    expect(find.text('🇷🇺 RU'), findsWidgets);
    expect(
      find.text('Saved selection; no currently verified exit.'),
      findsOneWidget,
    );
    await tester.tap(find.text('🇷🇺 RU').last);
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartExcludedCountries, isEmpty);
    expect(
      tester
          .widgetList<Checkbox>(find.byType(Checkbox))
          .every((checkbox) => checkbox.value == false),
      isTrue,
    );
  });

  testWidgets('preferred country picker saves one verified country', (
    tester,
  ) async {
    final container = await openSmartSettings(tester);
    await tester.tap(find.text('Any'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('🇩🇪 DE'));
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartPreferredCountry, 'DE');
    expect(container.read(appSettingProvider).smartExcludedCountries, ['RU']);
    expect(find.text('🇩🇪 DE'), findsOneWidget);
  });

  testWidgets('DNS toggle is independent of the automatic server switch', (
    tester,
  ) async {
    final container = await openSmartSettings(tester);
    container
        .read(appSettingProvider.notifier)
        .update((settings) => settings.copyWith(smartReconnect: false));
    await tester.pumpAndSettle();
    final dnsRow = find.ancestor(
      of: find.text('Smart DNS recovery').last,
      matching: find.byType(ListItem<dynamic>),
    );
    await tester.ensureVisible(dnsRow);
    await tester.tap(find.text('Smart DNS recovery').last);
    await tester.pumpAndSettle();
    expect(container.read(appSettingProvider).smartDnsRecovery, isFalse);
    expect(container.read(appSettingProvider).smartReconnect, isFalse);
  });

  testWidgets('shows actual smart selection and a visible manual mode', (
    tester,
  ) async {
    final container = ProviderContainer(
      overrides: [
        isStartProvider.overrideWithValue(true),
        smartStatusProvider.overrideWith(_Status.new),
      ],
    );
    addTearDown(container.dispose);
    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const TestApp(
          locale: Locale('en'),
          child: Scaffold(body: SmartConnectionCard()),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(
      find.textContaining('Any subscription: Actual server'),
      findsOneWidget,
    );
    expect(find.textContaining('Running'), findsOneWidget);
    container
        .read(appSettingProvider.notifier)
        .update((settings) => settings.copyWith(smartReconnect: false));
    await tester.pumpAndSettle();
    expect(find.textContaining('Manual mode'), findsOneWidget);
    expect(find.byTooltip('Resume automatic selection'), findsOneWidget);
  });

  testWidgets('ordinary Agree completes consent without typing a statement', (
    tester,
  ) async {
    bool? consent;
    await tester.pumpWidget(
      TestApp(
        locale: const Locale('en'),
        wrapInProviderScope: true,
        child: Builder(
          builder: (context) => Scaffold(
            body: TextButton(
              onPressed: () async {
                consent = await Navigator.of(context).push<bool>(
                  MaterialPageRoute(
                    builder: (_) => const DisclaimerView(requireConsent: true),
                  ),
                );
              },
              child: const Text('Open terms'),
            ),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Open terms'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Agree'));
    await tester.pumpAndSettle();
    expect(consent, isTrue);
    expect(find.byType(TextField), findsNothing);
  });
}
