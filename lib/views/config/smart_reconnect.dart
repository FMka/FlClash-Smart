import 'dart:io';

import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/common/service_probe.dart';
import 'package:fl_clash/common/smart_reconnect.dart';
import 'package:fl_clash/providers/providers.dart';
import 'package:fl_clash/ui/icons/icons.dart';
import 'package:fl_clash/views/dashboard/widgets/service_status.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:material_ui/material_ui.dart';

String smartPhaseLabel(BuildContext context, SmartStatus status) {
  final l = context.appLocalizations;
  if (status.manual) return l.smartManual;
  return switch (status.phase) {
    'stopped' => l.notRunning,
    'disabled' => l.disabled,
    'connected' => l.running,
    'partial-unavailable' || 'service-unavailable' => l.smartPartial,
    'unavailable' ||
    'internal-error' ||
    'network-unavailable' => l.smartUnavailable,
    _ => l.smartSearching,
  };
}

String _reasonLabel(BuildContext context, String reason) {
  final l = context.appLocalizations;
  if (reason.isEmpty) return '';
  if (reason == 'country-filter-conflict') return l.smartCountryConflict;
  if (reason.contains('country-excluded')) return l.smartCountryExcluded;
  if (reason.contains('preferred-country-unavailable')) {
    return l.smartCountryUnavailable;
  }
  if (reason.contains('country')) return '${l.country}: ${l.unknown}';
  for (final target in ServiceTarget.values) {
    if (reason.contains(target.id)) return '${target.label}: ${l.smartPartial}';
  }
  return l.smartUnavailable;
}

Future<void> resumeSmartAuto(WidgetRef ref) async {
  ref
      .read(appSettingProvider.notifier)
      .update((settings) => settings.copyWith(smartReconnect: true));
  if (!ref.read(isStartProvider)) return;
  await ref
      .read(setupActionProvider.notifier)
      .applyProfile(force: true, silence: true);
  await ref.read(smartStatusProvider.notifier).refresh();
}

class SmartConnectionCard extends ConsumerStatefulWidget {
  const SmartConnectionCard({super.key, this.showSettings = true});

  final bool showSettings;

  @override
  ConsumerState<SmartConnectionCard> createState() =>
      _SmartConnectionCardState();
}

class _SmartConnectionCardState extends ConsumerState<SmartConnectionCard> {
  bool _repairing = false;

  Future<void> _repair({bool resume = false}) async {
    if (_repairing) return;
    setState(() => _repairing = true);
    try {
      if (resume) {
        await resumeSmartAuto(ref);
      } else {
        final accepted = await ref
            .read(coreHandlerProvider)
            .smartReconnectCheck();
        if (!accepted && mounted) {
          dialogs.showNotifier(context.appLocalizations.smartRepairFailed);
        }
      }
      if (mounted) await ref.read(smartStatusProvider.notifier).refresh();
    } catch (error) {
      if (mounted) {
        dialogs.showNotifier(context.appLocalizations.smartRepairFailed);
      }
    } finally {
      if (mounted) setState(() => _repairing = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = context.appLocalizations;
    final settings = ref.watch(appSettingProvider);
    final status = ref.watch(smartStatusProvider);
    final running = ref.watch(isStartProvider);
    final group = status.groups.firstOrNull;
    final groupName =
        group?.name ??
        ref
            .watch(groupsProvider)
            .where((group) => group.hidden != true)
            .firstOrNull
            ?.name;
    final current = group?.current ?? ref.watch(selectedMapProvider)[groupName];
    final node = current == null || current.isEmpty || current == 'REJECT'
        ? l.smartSearching
        : current;
    final manual = !settings.smartReconnect || status.manual;
    final phase = !running
        ? l.notRunning
        : manual
        ? l.smartManual
        : smartPhaseLabel(context, status);
    return Card(
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    '${l.smartReconnect} · $phase',
                    style: AppTextStyle.subheadline.of(
                      context,
                      emphasized: true,
                    ),
                  ),
                  if (running)
                    Text(
                      groupName == null
                          ? l.smartNoGroup
                          : '$groupName: $node${group == null || group.country.isEmpty ? '' : ' · ${group.country}'}',
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: AppTextStyle.caption1.of(context),
                    ),
                  if (running && settings.smartDnsRecovery)
                    Text(
                      '${l.smartDnsRecovery}: ${_dnsPhaseLabel(context, status.dns.phase)}',
                      style: AppTextStyle.caption1.of(context),
                    ),
                ],
              ),
            ),
            if (running)
              IconButton(
                tooltip: manual ? l.smartResumeAuto : l.smartRestore,
                onPressed: _repairing ? null : () => _repair(resume: manual),
                icon: _repairing
                    ? const SizedBox.square(
                        dimension: 20,
                        child: CircularProgressIndicator(strokeWidth: 2),
                      )
                    : GlyphIcon(manual ? AppGlyphs.bolt : AppGlyphs.refresh),
              ),
            if (widget.showSettings)
              IconButton(
                tooltip: l.settings,
                onPressed: () =>
                    BaseNavigator.push(context, const SmartReconnectView()),
                icon: const GlyphIcon(AppGlyphs.settings),
              ),
          ],
        ),
      ),
    );
  }
}

class SmartReconnectView extends ConsumerWidget {
  const SmartReconnectView({super.key});

  Future<void> _export(WidgetRef ref) async {
    final journal = await ref.read(coreHandlerProvider).getSmartJournal();
    final path = await appPath.tempFilePath;
    await File(path).safeWriteAsString(journal);
    await picker.saveFileWithPath('FlClash-Smart-journal.log', path);
  }

  Widget _budget(BuildContext context, WidgetRef ref, {required bool mobile}) {
    final l = context.appLocalizations;
    final settings = ref.watch(appSettingProvider);
    final status = ref.watch(smartStatusProvider);
    final budget = mobile
        ? settings.smartMobileBudgetMiB
        : settings.smartWifiBudgetMiB;
    final used = (mobile ? status.mobileUsed : status.wifiUsed) / (1024 * 1024);
    final label = mobile ? l.smartMobileBudget : l.smartWifiBudget;
    return ListItem.input(
      title: Text(label),
      subtitle: Text('${used.toStringAsFixed(1)} / $budget MiB'),
      dialogTitle: label,
      value: '$budget',
      maxLength: 5,
      keyboardType: TextInputType.number,
      validator: (value) {
        final number = int.tryParse(value ?? '');
        return number == null || number < 0 || number > 10000
            ? l.smartBudgetInvalid
            : null;
      },
      onChanged: (value) {
        final number = int.tryParse(value ?? '');
        if (number == null || number < 0 || number > 10000) return;
        ref
            .read(appSettingProvider.notifier)
            .update(
              (settings) => mobile
                  ? settings.copyWith(smartMobileBudgetMiB: number)
                  : settings.copyWith(smartWifiBudgetMiB: number),
            );
      },
    );
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l = context.appLocalizations;
    final status = ref.watch(smartStatusProvider);
    return CommonScaffold(
      title: l.smartReconnect,
      iconActions: [
        IconButtonData(
          glyph: AppGlyphs.export,
          tooltip: l.smartJournal,
          onPressed: () => _export(ref),
        ),
      ],
      body: ListView(
        padding: const EdgeInsets.symmetric(
          horizontal: 16,
        ).copyWith(top: context.contentTopPadding, bottom: 16),
        children: [
          const SmartConnectionCard(showSettings: false),
          generateSection(
            items: [
              ConfigToggleItem(
                title: (l) => l.smartReconnect,
                selector: appSettingProvider.select(
                  (settings) => settings.smartReconnect,
                ),
                onChanged: (ref, enabled) => ref
                    .read(appSettingProvider.notifier)
                    .update(
                      (settings) => settings.copyWith(smartReconnect: enabled),
                    ),
              ),
              ListItem.open(
                title: Text(l.serviceManage),
                widget: const ServiceManageView(),
              ),
              const _SmartRoutingStatusItem(),
            ],
            footer: l.smartSettingsDesc,
          ),
          const _SmartCountrySettings(),
          generateSection(
            items: [
              ConfigToggleItem(
                title: (l) => l.smartDnsRecovery,
                selector: appSettingProvider.select(
                  (settings) => settings.smartDnsRecovery,
                ),
                onChanged: (ref, enabled) => ref
                    .read(appSettingProvider.notifier)
                    .update(
                      (settings) =>
                          settings.copyWith(smartDnsRecovery: enabled),
                    ),
              ),
              const _SmartDnsStatusItem(),
            ],
            footer: l.smartDnsRecoveryDesc,
          ),
          generateSection(
            items: [
              _budget(context, ref, mobile: true),
              _budget(context, ref, mobile: false),
            ],
            footer: l.smartBudgetDesc,
          ),
          generateSection(
            items: [
              ListItem(
                title: Text(l.smartDiagnosticTraffic),
                subtitle: Text(
                  '${(status.diagnosticUsed / (1024 * 1024)).toStringAsFixed(2)} MiB',
                ),
              ),
            ],
            footer: l.smartDiagnosticDesc,
          ),
          if (status.groups.isNotEmpty)
            generateSection(
              items: [
                for (final group in status.groups)
                  ListItem(
                    title: Text(group.name),
                    subtitle: Text(
                      '${l.smartLastCheck}: ${_checkedAt(group.lastChecked, l.unknown)}\n${l.smartSpeed}: ${group.lastSpeed <= 0 ? l.unknown : '${(group.lastSpeed / (1024 * 1024)).toStringAsFixed(2)} MiB/s'}\n${l.smartSpeedMeasuredAt}: ${_checkedAt('${group.speedAt}', l.unknown)}${group.speedSource == 'cached' ? ' · ${l.smartHistory}' : ''}\n${l.smartPool}: ${group.pool.length}${group.reason.isEmpty ? '' : '\n${_reasonLabel(context, group.reason)}'}',
                    ),
                  ),
              ],
            ),
        ],
      ),
    );
  }
}

String _countryLabel(String country) => '${country.countryFlagEmoji} $country';

class _SmartRoutingStatusItem extends ConsumerWidget {
  const _SmartRoutingStatusItem();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l = context.appLocalizations;
    final status = ref.watch(smartStatusProvider).routing;
    final source = switch (status.source) {
      'subscription' => l.smartRoutingSubscription,
      'builtin-ru' => l.smartRoutingBuiltin,
      'custom' => l.smartRoutingCustom,
      _ => l.unknown,
    };
    final updated = DateTime.tryParse(status.updatedAt);
    return ListItem(
      title: Text(l.smartRoutingSource),
      subtitle: Text(
        [
          source,
          if (status.version.isNotEmpty) status.version,
          if (updated != null)
            '${l.lastUpdated}: ${updated.toLocal().toString().split('.').first}',
          if (status.error.isNotEmpty) l.smartRoutingUpdateFailed,
        ].join('\n'),
      ),
    );
  }
}

class _SmartCountrySettings extends ConsumerWidget {
  const _SmartCountrySettings();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l = context.appLocalizations;
    final settings = ref.watch(appSettingProvider);
    final status = ref.watch(smartStatusProvider);
    final preferred = settings.smartPreferredCountry.trim().toUpperCase();
    final excluded = normalizeSmartCountries(settings.smartExcludedCountries);
    final countries = smartCountryOptions(settings, status.discoveredCountries);
    return generateSection(
      items: [
        ListItem<String>.options(
          title: Text(l.smartPreferredCountry),
          options: ['', ...countries],
          value: preferred,
          textBuilder: (country) =>
              country.isEmpty ? l.smartCountryAny : _countryLabel(country),
          onChanged: (country) => ref
              .read(appSettingProvider.notifier)
              .update(
                (settings) => settings.copyWith(smartPreferredCountry: country),
              ),
        ),
        ListItem.open(
          title: Text(l.smartExcludedCountries),
          subtitle: Text(
            excluded.isEmpty ? l.none : excluded.map(_countryLabel).join(', '),
          ),
          widget: const _SmartExcludedCountriesView(),
        ),
        ListItem(
          title: Text(l.smartCountryDiscovery),
          subtitle: Text(
            '${l.smartCountryVerified(status.discoveredCountries.length)} · ${l.smartCountryUnknown(status.unresolvedCountries)}',
          ),
        ),
        if (preferred.isNotEmpty && excluded.contains(preferred))
          ListItem(title: Text(l.smartCountryConflict)),
      ],
      footer: '${l.smartCountryStrict}\n${l.smartCountryDiscoveryDesc}',
    );
  }
}

class _SmartExcludedCountriesView extends ConsumerWidget {
  const _SmartExcludedCountriesView();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l = context.appLocalizations;
    final settings = ref.watch(appSettingProvider);
    final status = ref.watch(smartStatusProvider);
    final countries = smartCountryOptions(settings, status.discoveredCountries);
    final excluded = normalizeSmartCountries(settings.smartExcludedCountries);
    return CommonScaffold(
      title: l.smartExcludedCountries,
      body: ListView(
        padding: const EdgeInsets.symmetric(
          horizontal: 16,
        ).copyWith(top: context.contentTopPadding, bottom: 16),
        children: [
          generateSection(
            items: [
              for (final country in countries)
                ListItem.checkbox(
                  title: Text(_countryLabel(country)),
                  subtitle: status.discoveredCountries.contains(country)
                      ? null
                      : Text(l.smartCountryNotVerified),
                  value: excluded.contains(country),
                  onChanged: (selected) =>
                      ref.read(appSettingProvider.notifier).update((settings) {
                        final values = normalizeSmartCountries(
                          settings.smartExcludedCountries,
                        ).toSet();
                        if (selected == true) {
                          values.add(country);
                        } else {
                          values.remove(country);
                        }
                        return settings.copyWith(
                          smartExcludedCountries: values.toList()..sort(),
                        );
                      }),
                ),
              if (countries.isEmpty) ListItem(title: Text(l.smartCountryEmpty)),
            ],
            footer: l.smartCountryDiscoveryDesc,
          ),
        ],
      ),
    );
  }
}

String _dnsPhaseLabel(BuildContext context, String phase) {
  final l = context.appLocalizations;
  return switch (phase) {
    'disabled' => l.disabled,
    'stopped' => l.notRunning,
    'diagnosing' => l.smartDnsDiagnosing,
    'recovered' => l.smartDnsRecovered,
    'unavailable' => l.smartDnsUnavailable,
    _ => l.smartDnsObserving,
  };
}

String _dnsReasonLabel(BuildContext context, String reason) {
  final l = context.appLocalizations;
  return switch (reason) {
    'repeated-timeout' => l.smartDnsTimeout,
    'checking-resolvers' => l.smartDnsDiagnosing,
    'alternatives-unavailable' => l.smartDnsUnavailable,
    'policy-preserved' => l.smartDnsPolicyPreserved,
    'original-restored' => l.smartDnsOriginalRestored,
    'network-changed' => l.smartDnsNetworkChanged,
    'expired' => l.smartDnsExpired,
    'verification-failed' => l.smartDnsVerificationFailed,
    _ => '',
  };
}

class _SmartDnsStatusItem extends ConsumerWidget {
  const _SmartDnsStatusItem();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final l = context.appLocalizations;
    final status = ref.watch(smartStatusProvider).dns;
    final enabled = ref.watch(
      appSettingProvider.select((settings) => settings.smartDnsRecovery),
    );
    final running = ref.watch(isStartProvider);
    final phase = !enabled
        ? l.disabled
        : !running
        ? l.notRunning
        : _dnsPhaseLabel(context, status.phase);
    final scope = switch (status.scope) {
      'direct' => l.smartDnsDirect,
      'node' => l.smartDnsNode,
      'destination' => l.smartDnsDestination,
      _ => '',
    };
    final reason = _dnsReasonLabel(context, status.reason);
    return ListItem(
      title: Text(l.smartDnsStatus),
      subtitle: Text(
        [
          phase,
          status.source == 'recovery'
              ? l.smartDnsSourceRecovery
              : l.smartDnsSourceProfile,
          if (status.provider.isNotEmpty && status.provider != 'profile')
            status.provider,
          if (scope.isNotEmpty) scope,
          if (reason.isNotEmpty) reason,
          if (status.lastChecked > 0)
            '${l.smartLastCheck}: ${_checkedAt('${status.lastChecked}', l.unknown)}',
        ].join('\n'),
      ),
    );
  }
}

String _checkedAt(String value, String unknown) {
  final timestamp = int.tryParse(value);
  if (timestamp == null || timestamp <= 0) return unknown;
  return DateTime.fromMillisecondsSinceEpoch(
    timestamp,
  ).toLocal().toString().split('.').first;
}
