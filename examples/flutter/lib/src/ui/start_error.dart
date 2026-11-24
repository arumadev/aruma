import 'package:flutter/material.dart';

/// Shown instead of the app when it cannot start: aruma.json is wrong, or firebase_options.dart
/// is still the placeholder. Meant for the developer, so in English only.
class StartErrorApp extends StatelessWidget {
  const StartErrorApp({super.key, required this.error});

  final Object error;

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      debugShowCheckedModeBanner: false,
      theme: ThemeData(colorSchemeSeed: const Color(0xFF00696B), fontFamily: 'NotoSans'),
      home: Scaffold(
        body: SafeArea(
          child: ListView(
            padding: const EdgeInsets.all(24),
            children: [
              const Icon(Icons.settings_suggest_outlined, size: 48),
              const SizedBox(height: 16),
              Text('Team Board could not start', style: Theme.of(context).textTheme.headlineSmall),
              const SizedBox(height: 12),
              SelectableText('$error'),
              const SizedBox(height: 24),
              const Text(
                'Put the firebase_options.dart your Aruma project generates (or `flutterfire configure` '
                'on Firebase) in lib/, and check aruma.json. The README says how.',
              ),
            ],
          ),
        ),
      ),
    );
  }
}
