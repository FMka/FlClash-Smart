import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/providers/smart_profile_scan.dart';
import 'package:fl_clash/ui/icons/icons.dart';
import 'package:fl_clash/ui/typography.dart';
import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

class SmartProfileScanProgress extends ConsumerWidget {
  const SmartProfileScanProgress({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final scan = ref.watch(smartProfileScanProvider);
    if (scan.phase == 'idle') return const SizedBox.shrink();
    final l = context.appLocalizations;
    final action = ref.read(smartProfileScanProvider.notifier);
    final phase = switch (scan.phase) {
      'preparing' => l.smartImportScanPreparing,
      'complete' => l.smartImportScanComplete,
      'partial' => l.smartImportScanPartial,
      'cancelled' => l.smartImportScanCancelled,
      'failed' => l.smartImportScanFailed,
      _ => '',
    };
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          mainAxisSize: MainAxisSize.min,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    l.smartImportScan,
                    style: AppTextStyle.headline.of(context),
                  ),
                ),
                IconButton(
                  tooltip: scan.running ? l.cancel : l.close,
                  onPressed: scan.running ? action.cancel : action.dismiss,
                  icon: const GlyphIcon(AppGlyphs.close),
                ),
              ],
            ),
            Text(
              scan.label,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: AppTextStyle.subheadline.of(context),
            ),
            if (scan.total > 0)
              Text(
                l.smartImportScanProgress(
                  scan.checked,
                  scan.total,
                  scan.responded,
                  scan.countries.length,
                ),
                style: AppTextStyle.footnote.of(context),
              ),
            if (phase.isNotEmpty)
              Text(phase, style: AppTextStyle.footnote.of(context)),
            if (scan.unsupported > 0)
              Text(
                l.smartImportScanUnsupported(scan.unsupported),
                style: AppTextStyle.footnote.of(context),
              ),
            if (scan.queued > 0)
              Text(
                l.smartImportScanQueued(scan.queued),
                style: AppTextStyle.footnote.of(context),
              ),
            if (scan.running) ...[
              const SizedBox(height: 8),
              LinearProgressIndicator(
                value: scan.total > 0 ? scan.checked / scan.total : null,
              ),
            ],
          ],
        ),
      ),
    );
  }
}
