import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// The language and the theme, remembered on the device (shared_preferences: localStorage on the
/// web, SharedPreferences on Android, NSUserDefaults on iOS).
class SettingsController extends ChangeNotifier {
  SettingsController(this._prefs)
      : _locale = switch (_prefs.getString(_localeKey)) {
          final String code when supportedLanguages.contains(code) => Locale(code),
          _ => null,
        },
        _themeMode = ThemeMode.values.asNameMap()[_prefs.getString(_themeKey)] ?? ThemeMode.system;

  static const supportedLanguages = ['en', 'ar'];
  static const _localeKey = 'locale';
  static const _themeKey = 'themeMode';

  final SharedPreferences _prefs;
  Locale? _locale;
  ThemeMode _themeMode;

  /// Null: the device's language (English when it is neither English nor Arabic).
  Locale? get locale => _locale;
  ThemeMode get themeMode => _themeMode;

  Future<void> setLocale(Locale? locale) async {
    if (locale == _locale) return;
    _locale = locale;
    notifyListeners();
    if (locale == null) {
      await _prefs.remove(_localeKey);
    } else {
      await _prefs.setString(_localeKey, locale.languageCode);
    }
  }

  Future<void> setThemeMode(ThemeMode mode) async {
    if (mode == _themeMode) return;
    _themeMode = mode;
    notifyListeners();
    await _prefs.setString(_themeKey, mode.name);
  }
}
