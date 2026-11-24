import 'package:flutter/material.dart';
import 'package:flutter_localizations/flutter_localizations.dart';

import '../l10n/app_localizations.dart';
import 'data/repositories.dart';
import 'device.dart';
import 'settings.dart';
import 'ui/common.dart';
import 'ui/sign_in_page.dart';
import 'ui/teams_page.dart';

/// The app: themes, languages, and the screen for the current user.
class TeamBoardApp extends StatefulWidget {
  const TeamBoardApp({super.key, required this.backend, required this.settings, this.device = const Device()});

  final Backend backend;
  final SettingsController settings;
  final Device device;

  @override
  State<TeamBoardApp> createState() => _TeamBoardAppState();
}

class _TeamBoardAppState extends State<TeamBoardApp> {
  final _navigator = GlobalKey<NavigatorState>();

  @override
  Widget build(BuildContext context) {
    final settings = widget.settings;
    return Scope(
      backend: widget.backend,
      settings: settings,
      device: widget.device,
      child: ListenableBuilder(
        listenable: settings,
        builder: (context, _) => MaterialApp(
          navigatorKey: _navigator,
          onGenerateTitle: (context) => AppLocalizations.of(context).appTitle,
          debugShowCheckedModeBanner: false,
          locale: settings.locale,
          supportedLocales: AppLocalizations.supportedLocales,
          localizationsDelegates: const [
            AppLocalizations.delegate,
            GlobalMaterialLocalizations.delegate,
            GlobalWidgetsLocalizations.delegate,
            GlobalCupertinoLocalizations.delegate,
          ],
          themeMode: settings.themeMode,
          theme: buildTheme(Brightness.light),
          darkTheme: buildTheme(Brightness.dark),
          // The text font follows the language; the other one is the fallback for names and
          // messages written in it.
          builder: (context, child) {
            final arabic = Localizations.localeOf(context).languageCode == 'ar';
            final theme = Theme.of(context);
            return Session(
              navigatorKey: _navigator,
              child: Theme(
                data: theme.copyWith(
                  textTheme: theme.textTheme.apply(
                    fontFamily: arabic ? 'NotoSansArabic' : 'NotoSans',
                    fontFamilyFallback: [arabic ? 'NotoSans' : 'NotoSansArabic'],
                  ),
                ),
                child: child!,
              ),
            );
          },
          home: const AuthGate(),
        ),
      ),
    );
  }
}

ThemeData buildTheme(Brightness brightness) {
  final scheme = ColorScheme.fromSeed(seedColor: const Color(0xFF00696B), brightness: brightness);
  return ThemeData(
    colorScheme: scheme,
    fontFamily: 'NotoSans',
    fontFamilyFallback: const ['NotoSansArabic'],
    inputDecorationTheme: const InputDecorationTheme(border: OutlineInputBorder()),
    snackBarTheme: const SnackBarThemeData(behavior: SnackBarBehavior.floating),
  );
}

/// Shows the sign-in screen or the user's teams, following the session. A session restored at
/// start-up (after a reload on the web) goes straight to the teams.
class AuthGate extends StatelessWidget {
  const AuthGate({super.key});

  @override
  Widget build(BuildContext context) {
    final session = Session.stateOf(context);
    // Until Auth has looked for a saved session.
    if (session == null) return const Scaffold(body: LoadingView());
    final user = session.user;
    if (user == null) return const SignInPage();
    return TeamsPage(key: ValueKey(user.uid));
  }
}
