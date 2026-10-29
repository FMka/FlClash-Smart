import 'dart:async';

import 'package:fl_clash/core/controller.dart';
import 'package:fl_clash/core/interface.dart';
import 'package:fl_clash/core/method.dart';
import 'package:fl_clash/enum/enum.dart';
import 'package:fl_clash/models/models.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/providers/smart_profile_scan.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';

import '../helpers/test_profiles.dart';

class _Core extends Mock implements CoreHandlerInterface {}

class _Setup extends SetupAction {
  final built = <int>[];

  @override
  Future<BuiltProfile> getProfile({
    required SetupState setupState,
    required PatchClashConfig patchConfig,
  }) async {
    built.add(setupState.profileId!);
    return (
      yaml: 'overlay',
      md5: '',
      counts: (proxies: 1, groups: 1, rules: 0),
      base: SetupBase(path: '/profile/${setupState.profileId}', overlay: ''),
    );
  }

  @override
  Future<bool> setRunning(bool running, {bool initialize = false}) async =>
      throw StateError('Import must not start or stop VPN');

  @override
  Future<bool> applyProfile({
    bool silence = false,
    bool force = false,
    Future<void> Function()? preloadInvoke,
  }) async => throw StateError('Import must not apply a different profile');
}

SetupState _setupState(int id) => SetupState(
  profileId: id,
  profileLastUpdateDate: null,
  profileType: ProfileType.url,
  extendType: ExtendType.standard,
  rules: const [],
  proxyGroups: const [],
  addedRules: const [],
  script: null,
  overrides: const ProfileOverrides(),
);

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  late _Core core;
  late _Setup setup;
  late ProviderContainer container;
  late List<Profile> profiles;
  late List<Map<String, dynamic>> requests;

  setUp(() {
    core = _Core();
    setup = _Setup();
    profiles = [
      Profile.normal(label: 'First'),
      Profile.normal(label: 'Second'),
    ];
    requests = [];
    when(() => core.getSmartStatus()).thenAnswer((_) async => {});
    when(() => core.scanSmartProfile(any())).thenAnswer((invocation) async {
      final args = Map<String, dynamic>.from(
        invocation.positionalArguments.single as Map,
      );
      requests.add(args);
      return {
        'phase': 'complete',
        'scan-id': args['profile-id'],
        'checked': 1,
        'total': 1,
        'responded': 1,
        'countries': ['DE'],
      };
    });
    container = ProviderContainer(
      overrides: [
        profilesProvider.overrideWith(() => TestProfiles(profiles)),
        coreHandlerProvider.overrideWithValue(CoreController.scoped(core)),
        setupActionProvider.overrideWith(() => setup),
        for (final profile in profiles)
          setupStateProvider(
            profile.id,
          ).overrideWith((_) async => _setupState(profile.id)),
      ],
    );
    addTearDown(container.dispose);
    final currentProfileHold = container.listen(
      currentProfileIdProvider,
      (_, _) {},
    );
    addTearDown(currentProfileHold.close);
    container.read(initProvider.notifier).value = true;
    container.read(currentProfileIdProvider.notifier).value = profiles.first.id;
  });

  Future<void> flush() async {
    for (var index = 0; index < 8; index++) {
      await Future<void>.delayed(Duration.zero);
    }
  }

  test('scans imports serially without replacing the active profile', () async {
    final scan = container.read(smartProfileScanProvider.notifier);
    scan.request(profiles.first.id);
    scan.request(profiles.last.id);
    await flush();
    expect(setup.built, [profiles.first.id, profiles.last.id]);
    expect(
      requests
          .where((args) => args['operation'] == 'start')
          .map((args) => args['profile-id']),
      ['${profiles.first.id}', '${profiles.last.id}'],
    );
    expect(container.read(currentProfileIdProvider), profiles.first.id);
    expect(container.read(smartProfileScanProvider).countries, ['DE']);
    expect(container.read(smartProfileScanProvider).phase, 'complete');
  });

  test('cancels a late start by its identity before the next import', () async {
    final delayed = Completer<Map<String, dynamic>>();
    when(() => core.scanSmartProfile(any())).thenAnswer((invocation) {
      final args = Map<String, dynamic>.from(
        invocation.positionalArguments.single as Map,
      );
      requests.add(args);
      if (args['operation'] == 'start' &&
          args['profile-id'] == '${profiles.first.id}') {
        return delayed.future;
      }
      return Future.value({'phase': 'complete', 'scan-id': 'new'});
    });
    final scan = container.read(smartProfileScanProvider.notifier);
    scan.request(profiles.first.id);
    await flush();
    await scan.cancel();
    scan.request(profiles.last.id);
    delayed.complete({'phase': 'scanning', 'scan-id': 'old'});
    await flush();
    expect(requests[1], {
      'operation': 'cancel',
      'scan-id': 'old',
      'profile-id': '${profiles.first.id}',
    });
    expect(requests[2]['profile-id'], '${profiles.last.id}');
    expect(container.read(smartProfileScanProvider).phase, 'complete');
  });

  test('a deleted queued subscription is never prepared', () async {
    container.read(initProvider.notifier).value = false;
    final scan = container.read(smartProfileScanProvider.notifier);
    scan.request(profiles.first.id);
    scan.request(profiles.last.id);
    await container.read(profilesProvider.notifier).del(profiles.last.id);
    container.read(initProvider.notifier).value = true;
    await flush();
    expect(setup.built, [profiles.first.id]);
  });

  testWidgets('backgrounding cancels a scan and its pending queue', (
    tester,
  ) async {
    when(() => core.scanSmartProfile(any())).thenAnswer((invocation) async {
      final args = Map<String, dynamic>.from(
        invocation.positionalArguments.single as Map,
      );
      requests.add(args);
      return {'phase': 'scanning', 'scan-id': 'active'};
    });
    final scan = container.read(smartProfileScanProvider.notifier);
    scan.request(profiles.first.id);
    scan.request(profiles.last.id);
    await tester.pump();
    expect(container.read(smartProfileScanProvider).phase, 'scanning');
    container.read(appVisibleProvider.notifier).value = false;
    await tester.pump(const Duration(seconds: 5));
    expect(container.read(smartProfileScanProvider).phase, 'cancelled');
    expect(
      requests.where((args) => args['operation'] == 'cancel'),
      hasLength(1),
    );
    expect(requests.where((args) => args['operation'] == 'status'), isEmpty);
    expect(setup.built, [profiles.first.id]);
  });

  testWidgets('an initial network race retries only twice', (tester) async {
    when(() => core.scanSmartProfile(any())).thenAnswer((invocation) async {
      requests.add(
        invocation.positionalArguments.single as Map<String, dynamic>,
      );
      throw const CoreMethodException(
        code: 'operation_failed',
        message: 'network unavailable',
      );
    });
    container
        .read(smartProfileScanProvider.notifier)
        .request(profiles.first.id);
    await tester.pump();
    await tester.pump(const Duration(seconds: 1));
    await tester.pump(const Duration(seconds: 1));
    expect(requests, hasLength(3));
    expect(container.read(smartProfileScanProvider).phase, 'failed');
  });

  testWidgets('cancellation stops a delayed initial-network retry', (
    tester,
  ) async {
    when(() => core.scanSmartProfile(any())).thenAnswer((invocation) async {
      requests.add(
        invocation.positionalArguments.single as Map<String, dynamic>,
      );
      throw const CoreMethodException(
        code: 'operation_failed',
        message: 'network unavailable',
      );
    });
    final scan = container.read(smartProfileScanProvider.notifier);
    scan.request(profiles.first.id);
    await tester.pump();
    await scan.cancel();
    await tester.pump(const Duration(seconds: 2));
    expect(requests, hasLength(1));
    expect(container.read(smartProfileScanProvider).phase, 'cancelled');
  });
}
