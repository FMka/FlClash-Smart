import 'package:fl_clash/common/common.dart';
import 'package:fl_clash/widgets/widgets.dart';
import 'package:material_ui/material_ui.dart';

import 'add.dart';

Future<void> offerFirstSubscription() async {
  await dialogs.showCommonDialog<void>(
    child: const SubscriptionOnboardingDialog(),
  );
}

class SubscriptionOnboardingDialog extends StatelessWidget {
  const SubscriptionOnboardingDialog({super.key});

  @override
  Widget build(BuildContext context) {
    final l = context.appLocalizations;
    return CommonDialog(
      title: l.smartSubscriptionWelcome,
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(),
          child: Text(l.later),
        ),
        AddProfileMenu(
          builder: (open) =>
              TextButton(onPressed: open, child: Text(l.addProfile)),
        ),
      ],
      child: Text(l.smartSubscriptionWelcomeDesc),
    );
  }
}
