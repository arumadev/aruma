import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../data/repositories.dart';
import 'common.dart';
import 'sign_in_page.dart' show nameError;

/// The largest picture the Storage rules accept (storage.rules: under 2 MB).
const maxAvatarMb = 2;

/// Name, picture, language, theme, sign out.
class ProfilePage extends StatefulWidget {
  const ProfilePage({super.key});

  @override
  State<ProfilePage> createState() => _ProfilePageState();
}

class _ProfilePageState extends State<ProfilePage> {
  final _form = GlobalKey<FormState>();
  TextEditingController? _name;
  bool _saving = false;
  double? _upload;

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    _name ??= TextEditingController(text: UserScope.of(context).name);
  }

  @override
  void dispose() {
    _name?.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    if (!_form.currentState!.validate()) return;
    final l = AppLocalizations.of(context);
    setState(() => _saving = true);
    try {
      await Scope.backendOf(context).profiles.saveName(UserScope.of(context).uid, _name!.text.trim());
      if (mounted) showMessage(context, l.saved);
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  Future<void> _changePicture() async {
    final user = UserScope.of(context);
    final profiles = Scope.backendOf(context).profiles;
    final file = await Scope.deviceOf(context).pickFile(images: true);
    if (file == null || !mounted) return;
    if (!file.contentType.startsWith('image/')) {
      showError(context, const AppException(AppErrorKind.notAnImage));
      return;
    }
    if (file.bytes.length >= maxAvatarMb * 1024 * 1024) {
      showError(context, const AppException(AppErrorKind.fileTooLarge, '$maxAvatarMb'));
      return;
    }
    setState(() => _upload = 0);
    try {
      await profiles.uploadAvatar(user.uid, user.name, file.bytes, file.contentType, (p) {
        if (mounted) setState(() => _upload = p);
      });
    } catch (e) {
      if (mounted) showError(context, e);
    } finally {
      if (mounted) setState(() => _upload = null);
    }
  }

  Future<void> _removePicture(String url) async {
    final user = UserScope.of(context);
    try {
      await Scope.backendOf(context).profiles.removeAvatar(user.uid, user.name, url);
    } catch (e) {
      if (mounted) showError(context, e);
    }
  }

  Future<void> _signOut() async {
    final auth = Scope.backendOf(context).auth;
    // Back to the first screen, which turns into the sign-in screen.
    Navigator.of(context).popUntil((r) => r.isFirst);
    try {
      await auth.signOut();
    } catch (_) {
      // Signing out of a session that is already gone.
    }
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    final user = UserScope.of(context);
    final settings = Scope.settingsOf(context);
    final photo = user.profile?.photoUrl;
    return Scaffold(
      appBar: AppBar(title: Text(l.profile)),
      body: ListenableBuilder(
        listenable: settings,
        builder: (context, _) => ListView(
          padding: const EdgeInsets.all(24),
          children: [
            Center(child: Avatar(name: user.name, photoUrl: photo, radius: 48)),
            if (_upload != null) ...[
              const SizedBox(height: 12),
              LinearProgressIndicator(value: _upload),
              const SizedBox(height: 4),
              Text(l.uploading((_upload! * 100).round()), textAlign: TextAlign.center, style: theme.textTheme.labelSmall),
            ],
            const SizedBox(height: 12),
            Wrap(
              alignment: WrapAlignment.center,
              spacing: 8,
              children: [
                TextButton.icon(
                  onPressed: _upload == null ? _changePicture : null,
                  icon: const Icon(Icons.photo_camera_outlined),
                  label: Text(l.changePicture),
                ),
                if (photo != null)
                  TextButton.icon(
                    onPressed: _upload == null ? () => _removePicture(photo) : null,
                    icon: const Icon(Icons.delete_outline),
                    label: Text(l.removePicture),
                  ),
              ],
            ),
            const SizedBox(height: 8),
            Text(
              user.user.isGuest ? l.guestAccount : l.signedInAs(user.user.email ?? ''),
              textAlign: TextAlign.center,
              style: theme.textTheme.bodySmall,
            ),
            const SizedBox(height: 24),
            Form(
              key: _form,
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Expanded(
                    child: TextFormField(
                      controller: _name,
                      decoration: InputDecoration(labelText: l.nameLabel),
                      validator: (v) => nameError(l, v),
                      onFieldSubmitted: (_) => _save(),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Padding(
                    padding: const EdgeInsets.only(top: 8),
                    child: FilledButton(onPressed: _saving ? null : _save, child: Text(l.save)),
                  ),
                ],
              ),
            ),
            const SizedBox(height: 32),
            Text(l.language, style: theme.textTheme.titleSmall),
            const SizedBox(height: 8),
            SegmentedButton<String>(
              segments: [
                ButtonSegment(value: '', label: Text(l.languageDevice)),
                ButtonSegment(value: 'en', label: Text(l.languageEnglish)),
                ButtonSegment(value: 'ar', label: Text(l.languageArabic)),
              ],
              selected: {settings.locale?.languageCode ?? ''},
              onSelectionChanged: (s) => settings.setLocale(s.first.isEmpty ? null : Locale(s.first)),
              showSelectedIcon: false,
            ),
            const SizedBox(height: 24),
            Text(l.theme, style: theme.textTheme.titleSmall),
            const SizedBox(height: 8),
            SegmentedButton<ThemeMode>(
              segments: [
                ButtonSegment(value: ThemeMode.system, label: Text(l.themeSystem), icon: const Icon(Icons.brightness_auto)),
                ButtonSegment(value: ThemeMode.light, label: Text(l.themeLight), icon: const Icon(Icons.light_mode_outlined)),
                ButtonSegment(value: ThemeMode.dark, label: Text(l.themeDark), icon: const Icon(Icons.dark_mode_outlined)),
              ],
              selected: {settings.themeMode},
              onSelectionChanged: (s) => settings.setThemeMode(s.first),
              showSelectedIcon: false,
            ),
            const SizedBox(height: 40),
            OutlinedButton.icon(
              onPressed: _signOut,
              icon: const Icon(Icons.logout),
              label: Text(l.signOut),
              style: OutlinedButton.styleFrom(foregroundColor: theme.colorScheme.error),
            ),
          ],
        ),
      ),
    );
  }
}
