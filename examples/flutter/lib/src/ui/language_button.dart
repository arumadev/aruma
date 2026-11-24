import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import 'common.dart';

/// The language menu of the sign-in screen (the profile screen has the same choice).
class LanguageButton extends StatelessWidget {
  const LanguageButton({super.key});

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final settings = Scope.settingsOf(context);
    return PopupMenuButton<String>(
      icon: const Icon(Icons.translate),
      tooltip: l.language,
      onSelected: (code) => settings.setLocale(code.isEmpty ? null : Locale(code)),
      itemBuilder: (context) => [
        CheckedPopupMenuItem(value: '', checked: settings.locale == null, child: Text(l.languageDevice)),
        CheckedPopupMenuItem(value: 'en', checked: settings.locale?.languageCode == 'en', child: Text(l.languageEnglish)),
        CheckedPopupMenuItem(value: 'ar', checked: settings.locale?.languageCode == 'ar', child: Text(l.languageArabic)),
      ],
    );
  }
}
