import 'package:cloud_firestore/cloud_firestore.dart';
import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
import 'package:flutter/services.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'firebase_options.dart';
import 'src/app.dart';
import 'src/config.dart';
import 'src/data/firebase/firebase_backend.dart';
import 'src/settings.dart';
import 'src/ui/start_error.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  // On the web the screens are canvas; the semantics tree gives screen readers (and the browser
  // tests in e2e/) the buttons, fields and texts.
  if (kIsWeb) SemanticsBinding.instance.ensureSemantics();

  final settings = SettingsController(await SharedPreferences.getInstance());
  final AppConfig config;
  try {
    config = AppConfig.parse(await rootBundle.loadString('aruma.json'));
    // Everything about the project comes from firebase_options.dart: the one Aruma generates
    // (dashboard, Add app, Flutter), or the one `flutterfire configure` writes for Firebase.
    await Firebase.initializeApp(options: DefaultFirebaseOptions.currentPlatform);
    if (config.useAruma) {
      // Firestore is the one service whose host is set in code: the config files do not carry
      // it, and Auth and Storage are redirected by the native libraries and the web build step.
      FirebaseFirestore.instance.settings = Settings(host: config.firestoreHost, sslEnabled: config.firestoreSsl);
    }
  } catch (e) {
    runApp(StartErrorApp(error: e));
    return;
  }
  runApp(TeamBoardApp(backend: firebaseBackend(config), settings: settings));
}
