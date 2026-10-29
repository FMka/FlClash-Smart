import 'dart:async';

import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/providers/smart_profile_scan.dart';
import 'package:fl_clash/views/profiles/onboarding.dart';
import 'package:fl_clash/views/profiles/scan_progress.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';

import '../helpers/test_app.dart';
import '../helpers/test_profiles.dart';

class _Scan extends SmartProfileScanNotifier {
  _Scan(this.initial);

  final SmartProfileScanState initial;
  var cancelled = false;

  @override
  SmartProfileScanState build() => initial;

  @override
  Future<void> cancel({bool clearQueue = true}) async {
    cancelled = true;
    state = const SmartProfileScanState(phase: 'cancelled');
  }
}

void main() {
  testWidgets('first subscription can be deferred', (tester) async {
    var completed = false;
    await tester.pumpWidget(
      TestApp(
        wrapInProviderScope: true,
        child: Scaffold(
          body: TextButton(
            onPressed: () async {
              await offerFirstSubscription();
              completed = true;
            },
            child: const Text('Welcome'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Welcome'));
    await tester.pumpAndSettle();
    expect(
      find.text(currentAppLocalizations.smartSubscriptionWelcome),
      findsOne,
    );
    await tester.tap(find.text(currentAppLocalizations.later));
    await tester.pumpAndSettle();
    expect(completed, isTrue);
    expect(find.byType(SubscriptionOnboardingDialog), findsNothing);
  });

  testWidgets('first subscription uses the standard import choices', (
    tester,
  ) async {
    await tester.pumpWidget(
      TestApp(
        overrides: [
          profilesProvider.overrideWith(() => TestProfiles(const <Profile>[])),
        ],
        child: Scaffold(
          body: TextButton(
            onPressed: () => unawaited(offerFirstSubscription()),
            child: const Text('Welcome'),
          ),
        ),
      ),
    );
    await tester.tap(find.text('Welcome'));
    await tester.pumpAndSettle();
    final l = currentAppLocalizations;
    await tester.tap(find.text(l.addProfile));
    await tester.pumpAndSettle();
    expect(find.text(l.qrcode), findsOne);
    expect(find.text(l.file), findsOne);
    expect(find.text(l.link), findsOne);
    await tester.tap(find.text(l.link));
    await tester.pumpAndSettle();
    expect(find.text(l.importFromLink), findsOne);
  });

  testWidgets('scan progress reports work and allows cancellation', (
    tester,
  ) async {
    final scan = _Scan(
      const SmartProfileScanState(
        profileId: 1,
        label: 'Subscription',
        phase: 'scanning',
        checked: 3,
        total: 8,
        responded: 2,
        countries: ['DE', 'NL'],
      ),
    );
    await tester.pumpWidget(
      TestApp(
        overrides: [smartProfileScanProvider.overrideWith(() => scan)],
        child: const Scaffold(body: SmartProfileScanProgress()),
      ),
    );
    await tester.pump();
    final l = currentAppLocalizations;
    expect(find.text(l.smartImportScanProgress(3, 8, 2, 2)), findsOne);
    expect(find.byType(LinearProgressIndicator), findsOne);
    await tester.tap(find.byTooltip(l.cancel));
    await tester.pumpAndSettle();
    expect(scan.cancelled, isTrue);
    expect(find.text(l.smartImportScanCancelled), findsOne);
    expect(find.byType(LinearProgressIndicator), findsNothing);
  });
}
