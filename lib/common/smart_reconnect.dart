import 'package:fl_clash/common/service_probe.dart';
import 'package:fl_clash/models/models.dart';

List<String> smartServiceIds(AppSettingProps settings) {
  final targets = orderServiceTargets(settings.serviceOrder);
  final selected = targets
      .where((target) => !settings.disabledServices.contains(target.id))
      .map((target) => target.id)
      .toList();
  return selected.isEmpty
      ? targets.map((target) => target.id).toList()
      : selected;
}

Map<String, dynamic> smartReconnectSettings(AppSettingProps settings) => {
  'enabled': settings.smartReconnect,
  'allow-russian': settings.smartAllowRussian,
  'services': smartServiceIds(settings),
  'mobile-budget-mib': settings.smartMobileBudgetMiB.clamp(0, 10000),
  'wifi-budget-mib': settings.smartWifiBudgetMiB.clamp(0, 10000),
};

Map<String, dynamic> withSmartReconnectSettings(
  Map<String, dynamic> config,
  AppSettingProps settings,
) => {...config, 'smart-reconnect-settings': smartReconnectSettings(settings)};
