// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import 'dart:convert';

/// Thrown by [fromArumaUrl] for a URL that is not a Cloud Storage location it
/// can turn into a `gs://` URL naming the same object.
class NotStorageUrlException extends FormatException {
  /// The [url] that was refused.
  NotStorageUrlException(String url)
      : super(
          'not a Cloud Storage URL (gs://<bucket>/<name>, or '
          'https://<host>/v0/b/<bucket>/o/<name>)',
          url,
        );
}

const _v0Bucket = '/v0/b/';

/// Turns a Cloud Storage download URL into the `gs://<bucket>/<name>` that
/// `FirebaseStorage.refFromURL` reads back to the same object.
///
/// The unified form, which Aruma serves from the root of its host and the
/// native SDKs return when the app is built for Aruma (ADR-022 §16), is
/// `https://<aruma-host>/v0/b/<bucket>/o/<encoded name>?alt=media&token=<uuid>`.
/// firebase_storage parses an http(s) URL in Dart, and takes only
/// `firebasestorage.googleapis.com`, `localhost` and `10.0.2.2` as Firebase
/// hosts, so `refFromURL` throws on a link on Aruma's host:
///
/// ```dart
/// final link = await ref.getDownloadURL();
/// final same = FirebaseStorage.instance.refFromURL(fromArumaUrl(link));
/// ```
///
/// Any host is accepted (Aruma's, Google's with or without `:443`). The name
/// is percent-decoded (strictly, as UTF-8; `+` stays `+`) and written raw
/// after the bucket, because `refFromURL` passes a `gs://` path on as it is:
/// encoding it would name another object. A `gs://` URL comes back unchanged.
///
/// Throws [NotStorageUrlException] when [url] is neither, including the
/// prefixed form `<aruma>/firebasestorage.googleapis.com/v0/...`, which no
/// platform reads, and when the name has an empty segment or a leading or
/// trailing `/`: firebase_storage trims one leading and one trailing `/`, and
/// the native SDKs collapse `//`, so no reference names such an object, and
/// this throws rather than give another one.
String fromArumaUrl(String url) {
  final (bucket, name) = _location(url);
  if (name.startsWith('/') || name.endsWith('/') || name.contains('//')) {
    throw NotStorageUrlException(url);
  }
  return 'gs://$bucket/$name';
}

/// The bucket and the decoded object name of a `gs://` or download URL.
(String, String) _location(String url) {
  if (url.startsWith('gs://')) {
    final rest = url.substring('gs://'.length);
    final slash = rest.indexOf('/');
    if (slash <= 0 || slash + 1 == rest.length) {
      throw NotStorageUrlException(url);
    }
    return (rest.substring(0, slash), rest.substring(slash + 1));
  }
  final sep = url.indexOf('://');
  final scheme = sep < 0 ? '' : url.substring(0, sep).toLowerCase();
  if (scheme != 'https' && scheme != 'http') {
    throw NotStorageUrlException(url);
  }
  final Uri uri;
  try {
    uri = Uri.parse(url);
  } on FormatException {
    throw NotStorageUrlException(url);
  }
  if (uri.host.isEmpty) {
    throw NotStorageUrlException(url);
  }
  // The path as written: Uri.parse would remove dot segments and normalise
  // escapes, and the name must be read exactly as the server encoded it.
  final start = sep + 3;
  var end = url.length;
  for (final c in const ['?', '#']) {
    final i = url.indexOf(c, start);
    if (i >= 0 && i < end) end = i;
  }
  final slash = url.indexOf('/', start);
  if (slash < 0 || slash >= end) {
    throw NotStorageUrlException(url);
  }
  final path = url.substring(slash, end);
  if (!path.startsWith(_v0Bucket)) {
    throw NotStorageUrlException(url);
  }
  final rest = path.substring(_v0Bucket.length);
  final o = rest.indexOf('/o/');
  if (o <= 0 || o + 3 == rest.length) {
    throw NotStorageUrlException(url);
  }
  final bucket = _decode(rest.substring(0, o), url);
  final name = _decode(rest.substring(o + 3), url);
  if (bucket.contains('/')) {
    throw NotStorageUrlException(url);
  }
  return (bucket, name);
}

/// Strict percent-decoding of a path part as UTF-8 (no `+` for space).
String _decode(String s, String url) {
  final raw = utf8.encode(s);
  final out = <int>[];
  for (var i = 0; i < raw.length; i++) {
    final b = raw[i];
    if (b != 0x25 /* % */) {
      out.add(b);
      continue;
    }
    if (i + 2 >= raw.length) throw NotStorageUrlException(url);
    final hi = _hex(raw[i + 1]), lo = _hex(raw[i + 2]);
    if (hi < 0 || lo < 0) throw NotStorageUrlException(url);
    out.add(hi << 4 | lo);
    i += 2;
  }
  try {
    return utf8.decode(out);
  } on FormatException {
    throw NotStorageUrlException(url);
  }
}

int _hex(int c) {
  if (c >= 0x30 && c <= 0x39) return c - 0x30;
  if (c >= 0x61 && c <= 0x66) return c - 0x61 + 10;
  if (c >= 0x41 && c <= 0x46) return c - 0x41 + 10;
  return -1;
}
