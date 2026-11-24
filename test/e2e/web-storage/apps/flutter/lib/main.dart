// The web Storage e2e from Dart, signed in: firebase_auth on the web signs a user up, out and in
// with email and password, refreshes the ID token, updates and reloads the profile, signs in
// anonymously, and signs the first user in again; then the eleven operations of src/ops.js run with
// firebase_storage under e2e/<uid>/, which the rules give that user only, and the user is deleted.
// Both JS SDKs are the files `aruma-web flutter-web` rewrote (firebase-auth.js and
// firebase-storage.js). The results land in window.__done, as in the other apps: `auth` for the
// Auth steps, `results` for Storage's; the app draws nothing. When an Auth step fails the app stops
// there (measure.sh's control build, without the auth product, fails at the first one).
import 'dart:async';
import 'dart:convert';
import 'dart:js_interop';
import 'dart:js_interop_unsafe';
import 'dart:typed_data';

import 'package:firebase_auth/firebase_auth.dart';
import 'package:firebase_core/firebase_core.dart';
import 'package:firebase_storage/firebase_storage.dart';
import 'package:http/http.dart' as http;
import 'package:web/web.dart' as web;

Future<void> main() async {
  final auth = <Map<String, Object?>>[];
  final results = <Map<String, Object?>>[];
  try {
    final e2e = jsonDecode((await http.get(Uri.base.resolve('/e2e.json'))).body) as Map<String, dynamic>;
    await run(e2e, auth, results);
    done({'bundler': 'flutter', 'auth': auth, 'results': results});
  } catch (e, s) {
    done({'bundler': 'flutter', 'error': '$e\n$s', 'auth': auth, 'results': results});
  }
}

void done(Map<String, Object?> value) => globalContext.setProperty('__done'.toJS, value.jsify());

Future<void> run(Map<String, dynamic> e2e, List<Map<String, Object?>> authResults, List<Map<String, Object?>> results) async {
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
  final uid = await signIn(authResults);
  final storage = FirebaseStorage.instance;
  storage.setMaxOperationRetryTime(const Duration(seconds: 8));
  storage.setMaxUploadRetryTime(const Duration(seconds: 8));
  final dir = 'e2e/$uid/flutter-${DateTime.now().microsecondsSinceEpoch}';
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
  await authStep(authResults, 'auth.deleteUser', () async {
    await FirebaseAuth.instance.currentUser!.delete();
    if (FirebaseAuth.instance.currentUser != null) throw 'still signed in';
    return 'deleted';
  });
}

/// One Auth step: ok with its detail, or failed with the error's code. A failed step stops the app.
Future<T> authStep<T>(List<Map<String, Object?>> results, String op, Future<T> Function() fn) async {
  try {
    final value = await fn();
    results.add({'op': op, 'ok': true, 'detail': '$value'});
    return value;
  } on FirebaseAuthException catch (e) {
    results.add({'op': op, 'ok': false, 'detail': '${e.code}: ${e.message}'});
    rethrow;
  } catch (e) {
    results.add({'op': op, 'ok': false, 'detail': '$e'});
    rethrow;
  }
}

/// The Auth scenario; returns the uid of the email user, signed in at the end.
Future<String> signIn(List<Map<String, Object?>> results) async {
  final auth = FirebaseAuth.instance;
  // A throwaway account on the test's own Aruma: not a real person's credentials.
  final email = 'flutter-${DateTime.now().microsecondsSinceEpoch}@example.com';
  const password = 'e2e-password-1234';
  final uid = await authStep(results, 'auth.signUp', () async {
    final c = await auth.createUserWithEmailAndPassword(email: email, password: password);
    return c.user!.uid;
  });
  await authStep(results, 'auth.signOut', () async {
    await auth.signOut();
    if (auth.currentUser != null) throw 'still signed in';
    return 'signed out';
  });
  await authStep(results, 'auth.signInWithPassword', () async {
    final c = await auth.signInWithEmailAndPassword(email: email, password: password);
    if (c.user!.uid != uid || c.user!.email != email) throw 'another user: ${c.user!.uid}';
    return c.user!.uid;
  });
  await authStep(results, 'auth.wrongPassword', () async {
    try {
      await auth.signInWithEmailAndPassword(email: email, password: 'not-the-password');
    } on FirebaseAuthException catch (e) {
      if (e.code == 'wrong-password' || e.code == 'invalid-credential') return e.code;
      rethrow;
    }
    throw 'signed in with a wrong password';
  });
  await authStep(results, 'auth.refreshIdToken', () async {
    // The forced refresh is securetoken's /v1/token.
    final token = await auth.currentUser!.getIdToken(true);
    final claims = jsonDecode(utf8.decode(base64Url.decode(base64Url.normalize(token!.split('.')[1])))) as Map<String, dynamic>;
    if (claims['user_id'] != uid) throw 'token of ${claims['user_id']}';
    return 'token for $uid';
  });
  await authStep(results, 'auth.updateProfile', () async {
    await auth.currentUser!.updateDisplayName('Flutter e2e');
    await auth.currentUser!.reload();
    final name = auth.currentUser!.displayName;
    if (name != 'Flutter e2e') throw 'displayName $name';
    return name;
  });
  await authStep(results, 'auth.signInAnonymously', () async {
    await auth.signOut();
    final c = await auth.signInAnonymously();
    if (!c.user!.isAnonymous || c.user!.uid == uid) throw 'not a new anonymous user';
    return 'anonymous ${c.user!.uid}';
  });
  await authStep(results, 'auth.signInAgain', () async {
    await auth.signOut();
    final c = await auth.signInWithEmailAndPassword(email: email, password: password);
    if (c.user!.uid != uid) throw 'another user: ${c.user!.uid}';
    return uid;
  });
  return uid;
}
