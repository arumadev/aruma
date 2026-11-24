import 'dart:math';

import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import 'common.dart';
import 'language_button.dart';

/// Sign in, create an account, or continue as a guest.
class SignInPage extends StatefulWidget {
  const SignInPage({super.key});

  @override
  State<SignInPage> createState() => _SignInPageState();
}

class _SignInPageState extends State<SignInPage> {
  final _form = GlobalKey<FormState>();
  final _name = TextEditingController();
  final _email = TextEditingController();
  final _password = TextEditingController();
  bool _signUp = false;
  bool _busy = false;
  Object? _error;

  @override
  void dispose() {
    _name.dispose();
    _email.dispose();
    _password.dispose();
    super.dispose();
  }

  Future<void> _run(Future<void> Function() action) async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      await action();
      // Signed in: AuthGate replaces this screen.
    } catch (e) {
      if (mounted) setState(() => _error = e);
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  void _submit() {
    if (!_form.currentState!.validate()) return;
    final auth = Scope.backendOf(context).auth;
    final email = _email.text.trim();
    _run(() => _signUp
        ? auth.signUp(name: _name.text.trim(), email: email, password: _password.text)
        : auth.signIn(email: email, password: _password.text));
  }

  void _guest() {
    final code = (1000 + Random().nextInt(9000)).toString();
    final name = AppLocalizations.of(context).guestName(code);
    _run(() => Scope.backendOf(context).auth.signInAsGuest(name: name));
  }

  @override
  Widget build(BuildContext context) {
    final l = AppLocalizations.of(context);
    final theme = Theme.of(context);
    return Scaffold(
      appBar: AppBar(title: Text(l.appTitle), actions: const [LanguageButton()]),
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 420),
              child: Form(
                key: _form,
                child: AutofillGroup(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.stretch,
                    children: [
                      Icon(Icons.dashboard_customize_outlined, size: 56, color: theme.colorScheme.primary),
                      const SizedBox(height: 12),
                      Text(_signUp ? l.signUpTitle : l.signInTitle,
                          style: theme.textTheme.headlineSmall, textAlign: TextAlign.center),
                      const SizedBox(height: 24),
                      if (_signUp) ...[
                        TextFormField(
                          controller: _name,
                          decoration: InputDecoration(labelText: l.nameLabel),
                          textInputAction: TextInputAction.next,
                          autofillHints: const [AutofillHints.name],
                          validator: (v) => nameError(l, v),
                        ),
                        const SizedBox(height: 16),
                      ],
                      TextFormField(
                        controller: _email,
                        decoration: InputDecoration(labelText: l.emailLabel),
                        keyboardType: TextInputType.emailAddress,
                        textInputAction: TextInputAction.next,
                        autofillHints: const [AutofillHints.email],
                        validator: (v) {
                          final s = v?.trim() ?? '';
                          return RegExp(r'^[^@\s]+@[^@\s]+\.[^@\s]+$').hasMatch(s) ? null : l.emailInvalid;
                        },
                      ),
                      const SizedBox(height: 16),
                      TextFormField(
                        controller: _password,
                        decoration: InputDecoration(labelText: l.passwordLabel),
                        obscureText: true,
                        autofillHints: [_signUp ? AutofillHints.newPassword : AutofillHints.password],
                        onFieldSubmitted: (_) => _busy ? null : _submit(),
                        // The server judges the strength: a weak one comes back as an error.
                        validator: (v) => (v ?? '').isEmpty ? l.passwordRequired : null,
                      ),
                      if (_error != null) ...[
                        const SizedBox(height: 16),
                        Semantics(
                          liveRegion: true,
                          child: Text(errorText(context, _error!),
                              style: TextStyle(color: theme.colorScheme.error), textAlign: TextAlign.center),
                        ),
                      ],
                      const SizedBox(height: 24),
                      FilledButton(
                        onPressed: _busy ? null : _submit,
                        child: _busy
                            ? const SizedBox.square(dimension: 20, child: CircularProgressIndicator(strokeWidth: 2))
                            : Text(_signUp ? l.signUpButton : l.signInButton),
                      ),
                      const SizedBox(height: 8),
                      TextButton(
                        onPressed: _busy
                            ? null
                            : () => setState(() {
                                  _signUp = !_signUp;
                                  _error = null;
                                }),
                        child: Text(_signUp ? l.switchToSignIn : l.switchToSignUp),
                      ),
                      const Divider(height: 32),
                      OutlinedButton.icon(
                        onPressed: _busy ? null : _guest,
                        icon: const Icon(Icons.person_outline),
                        label: Text(l.guestButton),
                      ),
                    ],
                  ),
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// The rules want a name of 1 to 40 characters.
String? nameError(AppLocalizations l, String? value) {
  final s = value?.trim() ?? '';
  if (s.isEmpty) return l.nameRequired;
  if (s.length > 40) return l.nameTooLong(40);
  return null;
}
