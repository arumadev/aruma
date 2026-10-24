import 'dart:async';
import 'dart:convert';
import 'dart:io' hide BytesBuilder;
import 'dart:typed_data';

import 'package:aruma_storage/aruma_storage.dart';
import 'package:firebase_auth/firebase_auth.dart';
import 'package:firebase_storage/firebase_storage.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import 'guard.dart';

/// The e2e native plugin (../e2e_native): the native guard, and what the native build holds.
const native = MethodChannel('aruma_e2e');

/// One line per step, "✓ op  detail" or "✗ op  error".
class Result {
  final List<String> lines = [];
  int passed = 0;

  bool get allPassed => passed == lines.length;
}

/// Shows `Image.network(url)` in the app and completes when its first frame is decoded.
typedef ShowImage = Future<void> Function(String url);

/// A 1×1 PNG, for Image.network to decode.
final _pixel = base64.decode(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
);

/// The full operation list of the S0-B experiment and of the Android and iOS e2e, with the
/// Flutter API's own (putString, list pages, writeToFile, Image.network), signed in anonymously
/// under e2e/{uid}/. The download URL must equal the unified form literally (ADR-022 §16): a link
/// on Google's host is a failure, not a warning.
///
/// [storageUrl] is the origin the native build was given (no path).
Future<Result> operations(String storageUrl, SocketGuard guard, ShowImage showImage) async {
  final res = Result();
  final auth = FirebaseAuth.instance;
  final storage = _storage();
  final bucket = storage.app.options.storageBucket!;
  final small = utf8.encode('hello aruma\n');
  final big = Uint8List(3 * 1024 * 1024 + 17);
  for (var i = 0; i < big.length; i++) {
    big[i] = (i * 31 + 7) & 0xff;
  }
  final tmp = await Directory.systemTemp.createTemp('aruma-e2e');
  final bigFile = File('${tmp.path}/big-upload.bin')..writeAsBytesSync(big);
  late final String uid;
  late final Reference dir, smallRef, bigRef, fileRef, stringRef, pixelRef;
  String? link, pixelLink;

  await step(res, 'init', () async {
    uid = await _signIn(auth);
    dir = storage.ref('e2e/$uid');
    smallRef = dir.child('small.txt');
    bigRef = dir.child('big.bin');
    fileRef = dir.child('file.bin');
    stringRef = dir.child('string.txt');
    pixelRef = dir.child('pixel.png');
    final info = Map<String, Object?>.from(await native.invokeMethod('info') as Map);
    if (Platform.isAndroid) {
      // firebase-storage's NetworkRequest.PROD_BASE_URL, as dev.aruma.storage wrote it.
      _equal('$storageUrl/v0', info['prodBaseUrl'], 'PROD_BASE_URL');
      if (!'${info['notice']}'.contains('with "$storageUrl/v0"')) {
        throw StateError('the plugin\'s notice is not in the app: ${info['notice']}');
      }
    } else {
      // ArumaRedirect's check: the FirebaseStorage linked is the mirror's patched build.
      _equal('patched', info['storageBuild'], 'ArumaRedirect.firebaseStorageBuild');
    }
    return 'uid $uid, bucket $bucket, ${info.entries.where((e) => e.key != 'notice').map((e) => '${e.key} ${e.value}').join(', ')}';
  });
  if (res.passed == 0) return res;

  await step(res, 'upload.small', () async {
    final s = await smallRef.putData(small, SettableMetadata(contentType: 'text/plain'));
    return _uploaded(s, small.length);
  });
  await step(res, 'upload.resumable3MB', () async {
    return _uploaded(await bigRef.putData(big), big.length);
  });
  await step(res, 'upload.putFile3MB', () async {
    return _uploaded(await fileRef.putFile(bigFile), big.length);
  });
  await step(res, 'upload.putString', () async {
    final s = await stringRef.putString(base64.encode(small), format: PutStringFormat.base64);
    final got = _uploaded(s, small.length);
    _same(small, await stringRef.getData(), 'bytes');
    return '$got (base64)';
  });
  await step(res, 'getMetadata', () async {
    final m = await smallRef.getMetadata();
    _equal(small.length, m.size, 'size');
    _equal('text/plain', m.contentType, 'contentType');
    _equal(bucket, m.bucket, 'bucket');
    _equal(smallRef.fullPath, m.fullPath, 'fullPath');
    return 'size ${m.size}, ${m.contentType}';
  });
  await step(res, 'updateMetadata', () async {
    final m = await smallRef.updateMetadata(
      SettableMetadata(cacheControl: 'no-cache', customMetadata: {'flutter': 'yes'}),
    );
    _equal('no-cache', m.cacheControl, 'cacheControl');
    _equal('yes', m.customMetadata?['flutter'], 'customMetadata');
    _equal('text/plain', m.contentType, 'contentType kept');
    return 'cacheControl ${m.cacheControl}, customMetadata ${m.customMetadata}';
  });
  await step(res, 'getDownloadURL', () async {
    final got = await smallRef.getDownloadURL();
    link = got;
    // The unified form, literally: the server's own token on the origin the native build was given.
    final want =
        '$storageUrl/v0/b/${Uri.encodeComponent(bucket)}/o/'
        '${Uri.encodeComponent(smallRef.fullPath)}?alt=media&token=${await _token(smallRef, storageUrl)}';
    _equal(want, got, 'download URL');
    // firebase_storage parses an http(s) URL in Dart and knows only Google's host (and the
    // emulator's localhost and 10.0.2.2): its own link on Aruma throws. fromArumaUrl reads back.
    final threw = _throws(() => storage.refFromURL(got));
    final same = storage.refFromURL(fromArumaUrl(got));
    _equal(smallRef.bucket, same.bucket, 'refFromURL(fromArumaUrl(link)).bucket');
    _equal(smallRef.fullPath, same.fullPath, 'refFromURL(fromArumaUrl(link)).fullPath');
    return '${_redact(got)}; refFromURL(link) threw $threw; refFromURL(fromArumaUrl(link)) is ${same.fullPath}';
  });
  await step(res, 'download.getData3MB', () async {
    final got = await bigRef.getData(16 * 1024 * 1024);
    _same(big, got, 'getData');
    return '${got!.length} bytes, equal';
  });
  await step(res, 'download.writeToFile3MB', () async {
    final f = File('${tmp.path}/big-download.bin');
    final s = await fileRef.writeToFile(f);
    _same(big, f.readAsBytesSync(), 'writeToFile');
    return '${s.totalBytes} bytes, equal';
  });
  await step(res, 'listAll', () async {
    final r = await dir.listAll();
    final names = r.items.map((i) => i.name).toList()..sort();
    _equal('big.bin file.bin small.txt string.txt', names.join(' '), 'listAll');
    return names.join(', ');
  });
  await step(res, 'list.pages', () async {
    final names = <String>[];
    var pages = 0;
    String? token;
    do {
      final r = await dir.list(ListOptions(maxResults: 3, pageToken: token));
      names.addAll(r.items.map((i) => i.name));
      token = r.nextPageToken;
      pages++;
    } while (token != null && pages < 10);
    names.sort();
    _equal('big.bin file.bin small.txt string.txt', names.join(' '), 'list');
    _equal(2, pages, 'pages of 3');
    return '$pages pages: ${names.join(', ')}';
  });
  await step(res, 'downloadURL.HttpClient', () async {
    // The link outside the library: dart:io, as package:http and CachedNetworkImage use it.
    final before = guard.loopback.length;
    final c = HttpClient();
    try {
      final rsp = await (await c.getUrl(Uri.parse(link!))).close();
      _equal(200, rsp.statusCode, 'status');
      _same(small, await _readAll(rsp), 'body');
    } finally {
      c.close();
    }
    final sockets = guard.loopback.sublist(before);
    return '200 from ${Uri.parse(link!).authority}; dart:io sockets: ${sockets.join(', ')}';
  });
  await step(res, 'downloadURL.Image.network', () async {
    await pixelRef.putData(_pixel, SettableMetadata(contentType: 'image/png'));
    pixelLink = await pixelRef.getDownloadURL();
    if (!pixelLink!.startsWith('$storageUrl/v0/b/')) {
      throw StateError('not on $storageUrl: ${_redact(pixelLink!)}');
    }
    final before = guard.loopback.length;
    await showImage(pixelLink!).timeout(const Duration(seconds: 30));
    final sockets = guard.loopback.sublist(before);
    final port = Uri.parse(storageUrl).port;
    if (!sockets.any((s) => s.endsWith(':$port'))) {
      throw StateError('Image.network opened no socket to Aruma: $sockets');
    }
    return 'decoded the PNG from ${Uri.parse(pixelLink!).authority}; dart:io sockets: ${sockets.join(', ')}';
  });
  await step(res, 'delete', () async {
    for (final r in [smallRef, bigRef, fileRef, stringRef, pixelRef]) {
      await r.delete();
    }
    try {
      await smallRef.getMetadata();
      throw StateError('small.txt is still there');
    } on FirebaseException catch (e) {
      _equal('object-not-found', e.code, 'getMetadata after delete');
    }
    return '5 objects, then getMetadata: object-not-found';
  });
  await tmp.delete(recursive: true);
  return res;
}

/// Names that need encoding, round-tripped through their links: for each, the reference, and
/// refFromURL(fromArumaUrl(its download URL)), must name the same object and read the same bytes,
/// and refFromURL(link) without fromArumaUrl throws. Then names that no reference can hold
/// (an empty segment, a leading or trailing '/'), in the same unified form: fromArumaUrl refuses
/// each, rather than give another object.
Future<Result> names(String storageUrl) async {
  final res = Result();
  final storage = _storage();
  final uid = await _signIn(FirebaseAuth.instance);
  for (final name in ['a b+é.txt', 'صورة ١.png', '50% off?#&=1.txt']) {
    await step(res, 'name $name', () async {
      final ref = storage.ref('e2e/$uid/$name');
      final bytes = utf8.encode('the object $name');
      await ref.putData(bytes);
      try {
        final link = await ref.getDownloadURL();
        // Each SDK escapes the name itself (from the metadata's name), as it does on Google's
        // host: the link must be that SDK's spelling exactly, and it must still name the object
        // (fromArumaUrl, refFromURL and the GET below).
        final escape = Platform.isIOS ? _gcsEscapedIOS : Uri.encodeComponent;
        final want =
            '$storageUrl/v0/b/${escape(ref.bucket)}/o/${escape(ref.fullPath)}?alt=media&token=';
        if (!link.startsWith(want)) throw StateError('want $want…, got ${_redact(link)}');
        final threw = _throws(() => storage.refFromURL(link));
        final gs = fromArumaUrl(link);
        _equal('gs://${ref.bucket}/${ref.fullPath}', gs, 'fromArumaUrl(link)');
        final back = storage.refFromURL(gs);
        _equal(ref.bucket, back.bucket, 'bucket');
        _equal(ref.fullPath, back.fullPath, 'fullPath');
        _equal(name, back.name, 'name');
        for (final r in [ref, back]) {
          _same(bytes, await r.getData(), 'bytes through ${r.fullPath}');
        }
        final c = HttpClient();
        try {
          final rsp = await (await c.getUrl(Uri.parse(link))).close();
          _equal(200, rsp.statusCode, 'link status');
          _same(bytes, await _readAll(rsp), 'link body');
        } finally {
          c.close();
        }
        return 'same object and bytes from the reference and $gs, and from the link; '
            'refFromURL(link) threw $threw';
      } finally {
        await ref.delete();
      }
    });
  }
  final bucket = Uri.encodeComponent(storage.app.options.storageBucket!);
  for (final name in ['e2e/$uid//x.txt', '/e2e/$uid/x.txt', 'e2e/$uid/x/']) {
    await step(res, 'refused ${jsonEncode(name)}', () async {
      final link = '$storageUrl/v0/b/$bucket/o/${Uri.encodeComponent(name)}?alt=media&token=t';
      try {
        final gs = fromArumaUrl(link);
        throw StateError('fromArumaUrl gave $gs');
      } on NotStorageUrlException {
        // What a reference made from it would hold instead: firebase_storage trims the leading
        // and trailing '/', and the native SDKs collapse '//' on every call.
        final other = storage.refFromURL('gs://${storage.app.options.storageBucket}/$name');
        return 'NotStorageUrlException; refFromURL(gs://…) gives ${jsonEncode(other.fullPath)}';
      }
    });
  }
  // A production host, not this machine: refFromURL throws on it too, and fromArumaUrl reads it.
  await step(res, 'production host', () async {
    const link =
        'https://storage.example.com/v0/b/p.appspot.com/o/a%20b%2B%C3%A9.txt?alt=media&token=t';
    final threw = _throws(() => storage.refFromURL(link));
    final ref = storage.refFromURL(fromArumaUrl(link));
    _equal('p.appspot.com', ref.bucket, 'bucket');
    _equal('a b+é.txt', ref.fullPath, 'fullPath');
    return 'refFromURL(link) threw $threw; through fromArumaUrl: ${ref.bucket} ${ref.fullPath}';
  });
  return res;
}

/// The guards, both halves: each refuses (or records) a probe off this device, and then nothing
/// else in the run went anywhere but this device.
Future<Result> guards(SocketGuard guard, {required bool probeOnly}) async {
  final res = Result();
  if (probeOnly) {
    await step(res, 'guard.dart.selfTest', () async {
      final n = guard.outside.length;
      try {
        await Socket.connect('192.0.2.1', 80, timeout: const Duration(seconds: 2));
        throw StateError('connected to 192.0.2.1');
      } on SocketException {
        // Refused by the guard, before any packet.
      }
      _equal(n + 1, guard.outside.length, 'refusals recorded');
      guard.forget(1);
      return 'refused and recorded a socket to 192.0.2.1:80';
    });
    await step(res, 'guard.native.selfTest', () async {
      return '${await native.invokeMethod('selfTest')}';
    });
    return res;
  }
  await step(res, 'guard.dart', () async {
    if (guard.outside.isNotEmpty)
      throw StateError('Dart sockets off this device: ${guard.outside}');
    return '${guard.loopback.length} sockets, all to loopback';
  });
  await step(res, 'guard.native', () async {
    final g = Map<String, Object?>.from(await native.invokeMethod('guard') as Map);
    final outside = (g['outside'] as List).cast<String>();
    if (outside.isNotEmpty) throw StateError('native requests off this device: $outside');
    final redirected = (g['redirected'] as List? ?? const []).cast<String>();
    if (redirected.any((h) => h.contains('firebasestorage'))) {
      throw StateError('Storage requests through the run-time fallback: $redirected');
    }
    return '${g['summary']}';
  });
  return res;
}

Future<void> step(Result res, String op, Future<String> Function() fn) async {
  String line;
  try {
    final detail = await fn().timeout(const Duration(seconds: 120));
    res.passed++;
    line = '✓ $op  $detail';
  } catch (e) {
    line = '✗ $op  ${e.runtimeType}: $e';
  }
  res.lines.add(line);
  debugPrint('ARUMA_E2E op $line', wrapWidth: 100000);
}

FirebaseStorage _storage() {
  final s = FirebaseStorage.instance;
  // A refused request is retried until these run out (defaults: 2 and 10 minutes).
  s.setMaxOperationRetryTime(const Duration(seconds: 10));
  s.setMaxUploadRetryTime(const Duration(seconds: 10));
  s.setMaxDownloadRetryTime(const Duration(seconds: 10));
  return s;
}

Future<String> _signIn(FirebaseAuth auth) async {
  await auth.signOut();
  return (await auth.signInAnonymously()).user!.uid;
}

/// downloadTokens from the object's /v0 metadata, as the SDKs read it, with the user's ID token.
Future<String> _token(Reference ref, String storageUrl) async {
  final idToken = await FirebaseAuth.instance.currentUser!.getIdToken();
  final c = HttpClient();
  try {
    final req = await c.getUrl(
      Uri.parse(
        '$storageUrl/v0/b/${Uri.encodeComponent(ref.bucket)}/o/${Uri.encodeComponent(ref.fullPath)}',
      ),
    );
    req.headers.set('Authorization', 'Firebase $idToken');
    final rsp = await req.close();
    _equal(200, rsp.statusCode, 'metadata status');
    final m = jsonDecode(utf8.decode(await _readAll(rsp))) as Map<String, Object?>;
    return '${m['downloadTokens']}'.split(',').first;
  } finally {
    c.close();
  }
}

/// The type of what fn throws (an AssertionError in debug, a TypeError in release), or fails.
String _throws(void Function() fn) {
  try {
    fn();
  } catch (e) {
    return e.runtimeType.toString();
  }
  throw StateError('did not throw');
}

Future<Uint8List> _readAll(Stream<List<int>> s) async {
  final b = BytesBuilder(copy: false);
  await for (final chunk in s) {
    b.add(chunk);
  }
  return b.takeBytes();
}

/// _uploaded checks an upload's final snapshot against the [length] bytes sent, and describes it.
/// The object's size, from the server, is [length] on every platform. The SDKs count progress
/// differently: firebase-storage (Android) counts the object's bytes; FirebaseStorage (iOS) takes
/// GTMSessionUploadFetcher's counts, which add the resumable upload's first request, the JSON
/// metadata (gtm-session-fetcher's GTMSessionUploadFetcher.m: `totalSent = initialBodySent +
/// currentOffset + totalBytesSent`, `totalExpected = initialBodySent + fullUploadLength`). So
/// on iOS bytesTransferred and totalBytes are [length] plus that body, as they are against Google;
/// the snapshot must still be complete (bytesTransferred == totalBytes).
String _uploaded(TaskSnapshot s, int length) {
  _equal(TaskState.success, s.state, 'state');
  _equal(length, s.metadata?.size, 'metadata.size');
  _equal(s.totalBytes, s.bytesTransferred, 'bytesTransferred against totalBytes');
  if (Platform.isIOS) {
    if (s.totalBytes <= length) {
      throw StateError(
        'totalBytes ${s.totalBytes}: want $length plus the metadata request\'s body',
      );
    }
    return 'size $length, ${s.bytesTransferred} bytes counted with the metadata request';
  }
  _equal(length, s.bytesTransferred, 'bytes sent');
  return '${s.bytesTransferred} bytes';
}

/// FirebaseStorage (iOS) 12.19.3's StorageUtils.GCSEscapedString: percent-encodes every character
/// but these ("the list at cloud.google.com/storage/docs/json_api/ without &, ; and +"), so it
/// leaves `$ , = : @` as they are, where Uri.encodeComponent (and Android) encode them.
String _gcsEscapedIOS(String s) {
  const allowed = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~!\$\'()*,=:@';
  final b = StringBuffer();
  for (final c in utf8.encode(s)) {
    final ch = String.fromCharCode(c);
    b.write(
      c < 0x80 && allowed.contains(ch)
          ? ch
          : '%${c.toRadixString(16).toUpperCase().padLeft(2, '0')}',
    );
  }
  return b.toString();
}

String _redact(String link) => link.replaceAll(RegExp('token=[^&]*'), 'token=<redacted>');

void _equal(Object? want, Object? got, String what) {
  if (want != got) throw StateError('$what: want <$want>, got <$got>');
}

void _same(List<int> want, List<int>? got, String what) {
  if (got == null || !listEquals(want, got)) {
    throw StateError('$what: ${got?.length} bytes differ from the ${want.length} sent');
  }
}
