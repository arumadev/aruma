import 'dart:async';

import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../data/models.dart';
import '../data/repositories.dart';
import '../device.dart';
import '../settings.dart';

/// The backend and the settings, for every screen below the app.
class Scope extends InheritedWidget {
  const Scope({super.key, required this.backend, required this.settings, required this.device, required super.child});

  final Backend backend;
  final SettingsController settings;
  final Device device;

  static Scope _of(BuildContext context) => context.dependOnInheritedWidgetOfExactType<Scope>()!;
  static Backend backendOf(BuildContext context) => _of(context).backend;
  static SettingsController settingsOf(BuildContext context) => _of(context).settings;
  static Device deviceOf(BuildContext context) => _of(context).device;

  @override
  bool updateShouldNotify(Scope oldWidget) => backend != oldWidget.backend || settings != oldWidget.settings || device != oldWidget.device;
}

/// The session: the signed-in user (following Auth, a session restored at start-up included) and
/// their profile (users/{uid}), live. It sits above the navigator, so every screen reaches it.
class Session extends StatefulWidget {
  const Session({super.key, required this.navigatorKey, required this.child});

  final GlobalKey<NavigatorState> navigatorKey;
  final Widget child;

  /// Null while Auth has not answered yet; then the user, or no user.
  static SessionData? stateOf(BuildContext context) => context.dependOnInheritedWidgetOfExactType<SessionData>()?.ready;

  @override
  State<Session> createState() => _SessionState();
}

class _SessionState extends State<Session> {
  StreamSubscription<AppUser?>? _auth;
  StreamSubscription<Profile?>? _profileSub;
  bool _ready = false;
  AppUser? _user;
  // The last signed-in user, for the screens of that user still closing after a sign-out.
  AppUser? _last;
  Profile? _profile;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _auth ??= Scope.backendOf(context).auth.userChanges().listen(_onUser);
  }

  void _onUser(AppUser? user) {
    final switched = user?.uid != _user?.uid;
    setState(() {
      _ready = true;
      _user = user;
      _last = user ?? _last;
      if (switched && user != null) _profile = null;
    });
    if (!switched) return;
    _profileSub?.cancel();
    _profileSub = null;
    // Signed out (or another user): no screen of the last one stays open.
    widget.navigatorKey.currentState?.popUntil((r) => r.isFirst);
    if (user != null) {
      _profileSub = Scope.backendOf(context).profiles.watch(user.uid).listen(
            (p) => setState(() => _profile = p),
            onError: (Object _) {},
          );
    }
  }

  @override
  void dispose() {
    _auth?.cancel();
    _profileSub?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => SessionData(
        isReady: _ready,
        user: _user,
        lastUser: _last,
        profile: _profile,
        child: widget.child,
      );
}

class SessionData extends InheritedWidget {
  const SessionData({super.key, required this.isReady, required this.user, required this.lastUser, required this.profile, required super.child});

  final bool isReady;
  final AppUser? user;
  final AppUser? lastUser;
  final Profile? profile;

  SessionData? get ready => isReady ? this : null;

  @override
  bool updateShouldNotify(SessionData old) => isReady != old.isReady || user != old.user || profile != old.profile;
}

/// The signed-in user, for the screens that need one.
class UserScope {
  static UserData of(BuildContext context) {
    final s = context.dependOnInheritedWidgetOfExactType<SessionData>()!;
    return UserData(user: (s.user ?? s.lastUser)!, profile: s.profile);
  }
}

class UserData {
  const UserData({required this.user, required this.profile});

  final AppUser user;
  final Profile? profile;

  String get uid => user.uid;

  /// The name others see: the profile's, else Auth's display name, else the email's first part.
  String get name {
    final n = profile?.name ?? user.displayName;
    if (n != null && n.isNotEmpty) return n;
    return user.email?.split('@').first ?? '?';
  }
}

/// What the user reads when an operation failed.
String errorText(BuildContext context, Object error) {
  final l = AppLocalizations.of(context);
  final kind = error is AppException ? error.kind : AppErrorKind.unknown;
  return switch (kind) {
    AppErrorKind.wrongPassword => l.errorWrongPassword,
    AppErrorKind.weakPassword => l.errorWeakPassword,
    AppErrorKind.emailInUse => l.errorEmailInUse,
    AppErrorKind.invalidEmail => l.errorInvalidEmail,
    AppErrorKind.userNotFound => l.errorUserNotFound,
    AppErrorKind.tooManyRequests => l.errorTooManyRequests,
    AppErrorKind.permissionDenied => l.errorPermissionDenied,
    AppErrorKind.teamNotFound => l.errorTeamNotFound,
    AppErrorKind.fileTooLarge => l.errorFileTooLarge(int.tryParse((error as AppException).detail ?? '') ?? 0),
    AppErrorKind.notAnImage => l.errorNotAnImage,
    AppErrorKind.network => l.errorNetwork,
    AppErrorKind.unknown => l.errorUnknown(error is AppException ? (error.detail ?? '') : '$error'),
  };
}

void showError(BuildContext context, Object error) {
  final messenger = ScaffoldMessenger.of(context);
  messenger.hideCurrentSnackBar();
  messenger.showSnackBar(SnackBar(
    content: Text(errorText(context, error)),
    backgroundColor: Theme.of(context).colorScheme.error,
  ));
}

void showMessage(BuildContext context, String text) {
  final messenger = ScaffoldMessenger.of(context);
  messenger.hideCurrentSnackBar();
  messenger.showSnackBar(SnackBar(content: Text(text)));
}

class LoadingView extends StatelessWidget {
  const LoadingView({super.key});

  @override
  Widget build(BuildContext context) => Center(
        child: CircularProgressIndicator(semanticsLabel: AppLocalizations.of(context).loading),
      );
}

class EmptyView extends StatelessWidget {
  const EmptyView({super.key, required this.icon, required this.title, required this.hint, this.actions = const []});

  final IconData icon;
  final String title;
  final String hint;
  final List<Widget> actions;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Center(
      child: SingleChildScrollView(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 56, color: theme.colorScheme.primary),
            const SizedBox(height: 16),
            Text(title, style: theme.textTheme.titleLarge, textAlign: TextAlign.center),
            const SizedBox(height: 8),
            Text(hint, style: theme.textTheme.bodyMedium, textAlign: TextAlign.center),
            if (actions.isNotEmpty) ...[
              const SizedBox(height: 24),
              Wrap(spacing: 12, runSpacing: 12, alignment: WrapAlignment.center, children: actions),
            ],
          ],
        ),
      ),
    );
  }
}

class ErrorView extends StatelessWidget {
  const ErrorView({super.key, required this.error, this.onRetry});

  final Object error;
  final VoidCallback? onRetry;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(Icons.error_outline, size: 48, color: theme.colorScheme.error),
            const SizedBox(height: 12),
            Text(errorText(context, error), textAlign: TextAlign.center),
            if (onRetry != null) ...[
              const SizedBox(height: 16),
              OutlinedButton(onPressed: onRetry, child: Text(AppLocalizations.of(context).retry)),
            ],
          ],
        ),
      ),
    );
  }
}

/// A banner while the Realtime Database is disconnected (.info/connected), shown once it has
/// been down for a moment, so that the first connection at start-up does not flash it.
class OfflineBanner extends StatefulWidget {
  const OfflineBanner({super.key});

  @override
  State<OfflineBanner> createState() => _OfflineBannerState();
}

class _OfflineBannerState extends State<OfflineBanner> {
  static const _grace = Duration(seconds: 2);
  StreamSubscription<bool>? _sub;
  Timer? _timer;
  bool _offline = false;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _sub ??= Scope.backendOf(context).presence.connected().listen(_onConnected, onError: (Object _) {});
    // Not connected yet when the screen opens: counts as down from now.
    _timer ??= Timer(_grace, () => setState(() => _offline = true));
  }

  void _onConnected(bool connected) {
    _timer?.cancel();
    if (connected) {
      if (_offline) setState(() => _offline = false);
    } else {
      _timer = Timer(_grace, () => setState(() => _offline = true));
    }
  }

  @override
  void dispose() {
    _timer?.cancel();
    _sub?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return AnimatedSize(
      duration: const Duration(milliseconds: 200),
      child: !_offline
          ? const SizedBox(width: double.infinity)
          : Semantics(
              liveRegion: true,
              child: Container(
                width: double.infinity,
                color: scheme.errorContainer,
                padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
                child: Row(
                  children: [
                    Icon(Icons.cloud_off, size: 20, color: scheme.onErrorContainer),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Text(AppLocalizations.of(context).offlineBanner,
                          style: TextStyle(color: scheme.onErrorContainer)),
                    ),
                  ],
                ),
              ),
            ),
    );
  }
}

/// The user's picture, or their initial.
class Avatar extends StatelessWidget {
  const Avatar({super.key, required this.name, this.photoUrl, this.radius = 20});

  final String name;
  final String? photoUrl;
  final double radius;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final initial = name.isEmpty ? '?' : name.characters.first.toUpperCase();
    final fallback = CircleAvatar(
      radius: radius,
      backgroundColor: scheme.primaryContainer,
      child: Text(initial, style: TextStyle(color: scheme.onPrimaryContainer, fontSize: radius * 0.9)),
    );
    final url = photoUrl;
    if (url == null || url.isEmpty) return fallback;
    return ClipOval(
      child: Image.network(
        url,
        width: radius * 2,
        height: radius * 2,
        fit: BoxFit.cover,
        // On the web a cross-origin image needs CORS to be drawn on the canvas; without it the
        // browser's own <img> shows it (Storage buckets have no CORS rule by default).
        webHtmlElementStrategy: WebHtmlElementStrategy.fallback,
        errorBuilder: (_, _, _) => fallback,
      ),
    );
  }
}
