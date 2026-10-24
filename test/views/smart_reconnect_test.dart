import 'package:fl_clash/providers/providers.dart';
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
    'groups': [
      {
        'name': 'Any subscription',
        'mode': 'auto',
        'current': 'Actual server',
        'country': 'DE',
      },
    ],
  });
}

void main() {
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
