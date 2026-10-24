// The Flutter Cloud Storage e2e app (../../run.sh): the official firebase_storage and firebase_auth
// in production mode (no useStorageEmulator, no useAuthEmulator), configured only with the
// firebase_options.dart a real Aruma server generates. Storage reaches Aruma through the native
// SDKs built for it (dev.aruma.storage on Android, the patched FirebaseStorage mirror on iOS),
// Auth through aruma-redirect / ArumaRedirect. The app runs its scenario at launch, prints one
// "ARUMA_E2E op" line per step and "ARUMA_E2E_END" with the totals, and writes them to
// aruma-e2e.json in its temporary directory.
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';

import 'firebase_options.dart';
import 'guard.dart';
import 'scenario.dart';

/// The origin the native build was given (run.sh: --dart-define=ARUMA_STORAGE_URL=...).
const storageUrl = String.fromEnvironment('ARUMA_STORAGE_URL');

final _image = ValueNotifier<(String, Completer<void>)?>(null);

Future<void> _showImage(String url) {
  final done = Completer<void>();
  _image.value = (url, done);
  return done.future;
}

Future<void> main() async {
  final guard = SocketGuard();
  IOOverrides.global = guard;
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const _E2eApp());

  final all = <String, Result>{};
  String? fatal;
  try {
    if (storageUrl.isEmpty) throw StateError('built without --dart-define=ARUMA_STORAGE_URL');
    await Firebase.initializeApp(options: DefaultFirebaseOptions.currentPlatform);
    all['guard probes'] = await guards(guard, probeOnly: true);
    all['operations'] = await operations(storageUrl, guard, _showImage);
    all['names'] = await names(storageUrl);
    all['guards'] = await guards(guard, probeOnly: false);
  } catch (e, st) {
    fatal = '$e\n$st';
    debugPrint('ARUMA_E2E fatal $e', wrapWidth: 100000);
  }
  final passed = all.values.fold(0, (n, r) => n + r.passed);
  final total = all.values.fold(0, (n, r) => n + r.lines.length);
  final summary = {
    'mode': kReleaseMode ? 'release' : 'debug',
    'passed': passed,
    'total': total,
    'ok': fatal == null && passed == total && total > 0,
    'groups': {for (final e in all.entries) e.key: e.value.lines},
    'fatal': fatal,
  };
  File('${Directory.systemTemp.path}/aruma-e2e.json').writeAsStringSync(jsonEncode(summary));
  debugPrint(
    'ARUMA_E2E_END ${jsonEncode({'passed': passed, 'total': total, 'ok': summary['ok']})}',
  );
}

class _E2eApp extends StatelessWidget {
  const _E2eApp();

  @override
  Widget build(BuildContext context) {
    return Directionality(
      textDirection: TextDirection.ltr,
      child: Center(
        child: ValueListenableBuilder(
          valueListenable: _image,
          builder: (context, shown, _) {
            if (shown == null) return const Text('aruma e2e');
            final (url, done) = shown;
            // The download URL straight into Image.network: dart:io's HttpClient, which no
            // native redirect sees. It must load from Aruma.
            return Image.network(
              url,
              width: 64,
              height: 64,
              frameBuilder: (context, child, frame, _) {
                if (frame != null && !done.isCompleted) done.complete();
                return child;
              },
              errorBuilder: (context, error, _) {
                if (!done.isCompleted) done.completeError(error);
                return Text('$error');
              },
            );
          },
        ),
      ),
    );
  }
}
