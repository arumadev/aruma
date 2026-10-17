// Dart-level checks of Aruma's firebase_options.dart: it compiles, selects the right options per
// platform, and Firebase.initializeApp accepts them (with the native side mocked).
import 'dart:convert';
import 'dart:io';

import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_core_platform_interface/test.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:smoke/firebase_options.dart';

void main() {
  final expected = jsonDecode(File('expected.json').readAsStringSync()) as Map<String, dynamic>;
  TestWidgetsFlutterBinding.ensureInitialized();
  setupFirebaseCoreMocks();

  for (final (platform, key) in [
    (TargetPlatform.android, 'android'),
    (TargetPlatform.iOS, 'ios'),
  ]) {
    test('$key options carry the $key app', () async {
      debugDefaultTargetPlatformOverride = platform;
      addTearDown(() => debugDefaultTargetPlatformOverride = null);
      final options = DefaultFirebaseOptions.currentPlatform;
      final app = expected[key] as Map<String, dynamic>;
      expect(options.appId, app['appId']);
      expect(options.apiKey, app['apiKey']);
      expect(options.projectId, expected['projectId']);
      expect(options.messagingSenderId, expected['projectNumber']);
      expect(options.storageBucket, expected['storageBucket']);
      expect(options.databaseURL, expected['databaseURL']);
      if (key == 'ios') expect(options.iosBundleId, app['namespace']);

      // The mocked native side returns canned options, so only completion is checked here; the
      // integration test hands these options to the real native SDK.
      final fb = await Firebase.initializeApp(name: key, options: options);
      expect(fb.name, key);
    });
  }

  test('unconfigured platforms fail with a clear message', () {
    debugDefaultTargetPlatformOverride = TargetPlatform.windows;
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    expect(() => DefaultFirebaseOptions.currentPlatform, throwsA(isA<UnsupportedError>()));
  });

  test('web options are declared', () {
    final web = expected['web'] as Map<String, dynamic>;
    expect(DefaultFirebaseOptions.web.appId, web['appId']);
    expect(DefaultFirebaseOptions.web.authDomain, expected['authDomain']);
  });
}
