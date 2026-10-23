// S0-B on Flutter (Android): firebase_storage calls the native firebase-storage, which
// aruma-redirect (installed by MainActivity before the engine starts) sends to the stub. The
// download URL is then also opened with dart:io's HttpClient, which is not the platform's HTTP
// stack. The mode comes from the activity (intent extra) over the "s0path" channel; the results are
// printed one operation per line ("S0PATH_OP {json}", then "S0PATH_END <scenario>"), which
// ../flutter.sh reads from logcat.
import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_storage/firebase_storage.dart';
import 'package:flutter/services.dart';
import 'package:flutter/widgets.dart';

const channel = MethodChannel('s0path');

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  runApp(const Directionality(textDirection: TextDirection.ltr, child: Text('Aruma S0-B storage path')));
  final mode = await channel.invokeMethod<String>('mode') ?? 'prod-spike';
  final stub = Uri.parse(await channel.invokeMethod<String>('stub') ?? 'http://127.0.0.1:8820');
  final results = <Map<String, Object?>>[];
  Future<void> step(String op, Future<Object?> Function() fn) async {
    try {
      final d = await fn().timeout(const Duration(seconds: 90));
      results.add({'op': op, 'ok': true, 'detail': '${d ?? ''}'});
    } catch (e) {
      var s = '$e'.replaceAll('\n', ' ');
      if (s.length > 240) s = s.substring(0, 240);
      results.add({'op': op, 'ok': false, 'detail': s});
    }
  }

  final app = await Firebase.initializeApp(
    name: 's0path',
    options: const FirebaseOptions(
      apiKey: 'AIzaS0pathNotARealKey',
      appId: '1:1:android:s0path',
      messagingSenderId: '1',
      projectId: 's0path',
      storageBucket: 's0path.appspot.com',
    ),
  );
  final storage = FirebaseStorage.instanceFor(app: app);
  // automaticHostMapping would turn 127.0.0.1 into 10.0.2.2; the stub is on the device's own
  // loopback (adb reverse), where the guard lets it through.
  if (mode == 'emulator') await storage.useStorageEmulator('127.0.0.1', stub.port, automaticHostMapping: false);
  // A blocked request is retried until these run out (defaults: 2 and 10 minutes).
  storage.setMaxOperationRetryTime(const Duration(seconds: 4));
  storage.setMaxUploadRetryTime(const Duration(seconds: 4));
  storage.setMaxDownloadRetryTime(const Duration(seconds: 4));
  results.add({'op': 'init', 'ok': true, 'detail': 'mode=$mode'});

  final small = Uint8List.fromList(utf8.encode('hello aruma\n'));
  final big = Uint8List(3 * 1024 * 1024 + 17)..fillRange(0, 3 * 1024 * 1024 + 17, 7);
  final root = storage.ref();
  final smallRef = root.child('s0/small.txt'), bigRef = root.child('s0/big.bin');
  String? url;
  String redact(String u) => u.replaceAll(RegExp(r'token=[^&]*'), 'token=<redacted>');

  await step('upload.small', () async => (await smallRef.putData(small, SettableMetadata(contentType: 'text/plain'))).bytesTransferred);
  await step('upload.resumable3MB', () async => (await bigRef.putData(big)).bytesTransferred);
  await step('getMetadata', () async => 'size=${(await smallRef.getMetadata()).size}');
  await step('getDownloadURL', () async => redact(url = await smallRef.getDownloadURL()));
  await step('download.getData', () async => '${(await bigRef.getData(16 * 1024 * 1024))?.length} bytes');
  await step('list', () async => '${(await root.child('s0').listAll()).items.length} items');
  await step('downloadURL.dart:io HttpClient', () async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 5);
    final res = await (await client.getUrl(Uri.parse(url!))).close();
    final n = await res.fold<int>(0, (a, b) => a + b.length);
    return '${res.statusCode} $n bytes from ${Uri.parse(url!).host}';
  });
  await step('delete', () async {
    await smallRef.delete();
    await bigRef.delete();
    return '';
  });
  // One line per operation: Flutter splits a long print into ~1000-character logcat lines.
  for (final r in results) {
    // ignore: avoid_print
    print('S0PATH_OP ${jsonEncode(r)}');
  }
  // ignore: avoid_print
  print('S0PATH_END flutter-android-$mode');
}
