// The eleven operations of the web Storage e2e (src/ops.js), from Dart: firebase_storage on the web,
// whose JS SDK is the file `aruma-web flutter-web` rewrote. No user signs in (Flutter web has no
// Auth redirect here), so the app works under open/, which the rules leave open. The results land
// in window.__done, as in the other apps; the app draws nothing.
import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:js_interop_unsafe';
import 'dart:typed_data';

import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_storage/firebase_storage.dart';
import 'package:http/http.dart' as http;
import 'package:web/web.dart' as web;

Future<void> main() async {
  final results = <Map<String, Object?>>[];
  try {
    final e2e = jsonDecode((await http.get(Uri.base.resolve('/e2e.json'))).body) as Map<String, dynamic>;
    await run(e2e, results);
    done({'bundler': 'flutter', 'results': results});
  } catch (e, s) {
    done({'bundler': 'flutter', 'error': '$e\n$s', 'results': results});
  }
}

void done(Map<String, Object?> value) => globalContext.setProperty('__done'.toJS, value.jsify());

Future<void> run(Map<String, dynamic> e2e, List<Map<String, Object?>> results) async {
  final aruma = e2e['aruma'] as String;
  final bucket = e2e['bucket'] as String;
  final c = e2e['config'] as Map<String, dynamic>;
  await Firebase.initializeApp(
    options: FirebaseOptions(
      apiKey: c['apiKey'] as String,
      appId: c['appId'] as String,
      messagingSenderId: c['messagingSenderId'] as String,
      projectId: c['projectId'] as String,
      storageBucket: c['storageBucket'] as String,
      authDomain: c['authDomain'] as String?,
    ),
  );
  final storage = FirebaseStorage.instance;
  storage.setMaxOperationRetryTime(const Duration(seconds: 8));
  storage.setMaxUploadRetryTime(const Duration(seconds: 8));
  final dir = 'open/flutter-${DateTime.now().microsecondsSinceEpoch}';
  Reference ref(String p) => storage.ref('$dir/$p');
  final host = Uri.parse(aruma).host + (Uri.parse(aruma).hasPort ? ':${Uri.parse(aruma).port}' : '');
  String? notUnified(String u, String path) {
    final want = '$aruma/v0/b/${Uri.encodeComponent(bucket)}/o/${Uri.encodeComponent('$dir/$path')}?alt=media&token=';
    return u.startsWith(want) ? null : 'not the unified link: ${u.replaceAll(RegExp(r'token=[^&]*'), 'token=…')}';
  }

  Future<void> step(String op, Future<(String, bool)> Function() fn) async {
    try {
      final (detail, warn) = await fn();
      results.add({'op': op, 'ok': !warn, 'warn': warn, 'detail': detail});
    } catch (e) {
      results.add({'op': op, 'ok': false, 'detail': '$e'});
    }
  }

  final small = Uint8List.fromList(utf8.encode('hello aruma\n'));
  final big = Uint8List.fromList(List.generate(3 * 1024 * 1024 + 17, (i) => i % 251));
  final png = base64Decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==');
  late String url;

  await step('init', () async {
    // The JS instance behind FirebaseStorage.instance: getStorage() of the default app.
    final app = (globalContext['firebase_core'] as JSObject).callMethod<JSObject>('getApp'.toJS);
    final js = (globalContext['firebase_storage'] as JSObject).callMethod<JSObject>('getStorage'.toJS, app);
    final h = (js['host'] as JSString).toDart;
    final p = (js['_protocol'] as JSString).toDart;
    return ('host=$h protocol=$p', h != host || p != Uri.parse(aruma).scheme);
  });
  await step('upload.small', () async {
    final s = await ref('small.txt').putData(small, SettableMetadata(contentType: 'text/plain'));
    return ('${s.totalBytes} bytes', false);
  });
  await step('upload.resumable3MB', () async {
    final s = await ref('big.bin').putData(big, SettableMetadata(contentType: 'application/octet-stream'));
    return ('${s.totalBytes} bytes', false);
  });
  await step('getMetadata', () async {
    final m = await ref('small.txt').getMetadata();
    if (m.size != 12 || m.contentType != 'text/plain') throw 'size ${m.size} ${m.contentType}';
    return ('size=${m.size}', false);
  });
  await step('getDownloadURL', () async {
    url = await ref('small.txt').getDownloadURL();
    final bad = notUnified(url, 'small.txt');
    return (bad ?? 'unified', bad != null);
  });
  await step('download.getBytes', () async {
    // On the web, getData is getDownloadURL and then package:http (window.fetch) on the link.
    final b = await ref('big.bin').getData(8 * 1024 * 1024);
    if (b == null || b.length != big.length) throw 'got ${b?.length} bytes';
    for (var i = 0; i < b.length; i++) {
      if (b[i] != big[i]) throw 'byte $i differs';
    }
    return ('${b.length} bytes, equal', false);
  });
  await step('download.getBlob', () async {
    final b = await ref('small.txt').getData();
    if (b == null || utf8.decode(b) != 'hello aruma\n') throw 'got ${b?.length} bytes';
    return ('${b.length} bytes, equal', false);
  });
  await step('list', () async {
    final l = await storage.ref(dir).list();
    final names = (l.items.map((i) => i.name).toList()..sort()).join(',');
    if (names != 'big.bin,small.txt') throw 'items $names';
    return (names, false);
  });
  await step('downloadURL.fetch', () async {
    final r = await http.get(Uri.parse(url));
    if (r.statusCode != 200 || r.body != 'hello aruma\n') throw '${r.statusCode}';
    return ('200 from ${Uri.parse(url).host}', false);
  });
  await step('downloadURL.img', () async {
    await ref('pixel.png').putData(png, SettableMetadata(contentType: 'image/png'));
    final u = await ref('pixel.png').getDownloadURL();
    final bad = notUnified(u, 'pixel.png');
    final img = web.HTMLImageElement()..id = 'img';
    final loaded = Completer<int>();
    img.onLoad.listen((_) => loaded.complete(img.naturalWidth));
    img.onError.listen((_) => loaded.completeError('<img> failed to load from ${Uri.parse(u).host}'));
    img.src = u;
    web.document.body!.append(img);
    final width = await loaded.future;
    return ('${bad ?? 'unified'}, width $width', bad != null || width != 1);
  });
  await step('delete', () async {
    for (final p in ['small.txt', 'big.bin', 'pixel.png']) {
      await ref(p).delete();
    }
    try {
      await ref('small.txt').getMetadata();
    } on FirebaseException catch (e) {
      if (e.code == 'object-not-found') return ('gone', false);
      rethrow;
    }
    throw 'still there';
  });
}
