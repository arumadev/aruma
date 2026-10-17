// Runs on a device or simulator: Firebase.initializeApp hands Aruma's options to the native
// Firebase SDK, which validates the app id and API key. Network access is refused on the Dart
// side and the native SDK has nothing to contact during initialisation.
import 'dart:io';

import 'package:firebase_core/firebase_core.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:integration_test/integration_test.dart';
import 'package:smoke/firebase_options.dart';

class _NoNetwork extends HttpOverrides {
  final attempts = <String>[];

  @override
  HttpClient createHttpClient(SecurityContext? context) {
    attempts.add('HttpClient');
    throw StateError('network access blocked by the smoke test');
  }
}

void main() {
  IntegrationTestWidgetsFlutterBinding.ensureInitialized();
  final guard = _NoNetwork();
  HttpOverrides.global = guard;

  testWidgets('Firebase.initializeApp accepts the Aruma options natively', (tester) async {
    final app = await Firebase.initializeApp(options: DefaultFirebaseOptions.currentPlatform);
    expect(app.options.projectId, DefaultFirebaseOptions.currentPlatform.projectId);
    expect(Firebase.apps, isNotEmpty);
    await tester.pump(const Duration(seconds: 2));
    expect(guard.attempts, isEmpty);
  });
}
