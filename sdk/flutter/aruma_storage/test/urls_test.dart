// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import 'dart:convert';
import 'dart:math';

import 'package:aruma_storage/aruma_storage.dart';
import 'package:test/test.dart';

void main() {
  group('fromArumaUrl', () {
    test('decodes the name and writes it raw', () {
      const cases = {
        // The unified form on Aruma, as the patched SDKs return it.
        'https://aruma.example.com/v0/b/p.appspot.com/o/e2e%2Fa%20b%2B%C3%A9.txt?alt=media&token=T':
            'gs://p.appspot.com/e2e/a b+é.txt',
        'http://127.0.0.1:9450/v0/b/p.appspot.com/o/%D8%B5%D9%88%D8%B1%D8%A9%20%D9%A1.png?alt=media&token=T':
            'gs://p.appspot.com/صورة ١.png',
        // Google's, as the official iOS SDK returns it (with :443), and '+' that is not a space.
        'https://firebasestorage.googleapis.com:443/v0/b/p.appspot.com/o/a+b%25c?alt=media':
            'gs://p.appspot.com/a+b%c',
        // Escapes in either case, '?' and '#' in the name, a fragment, an upper-case scheme.
        'HTTPS://Aruma.Example.com/v0/b/p/o/50%25%20off%3F%23%2a.txt#frag': 'gs://p/50% off?#*.txt',
        // Dot segments are names like any other: kept, not resolved.
        'https://a.example/v0/b/p/o/a%2F.%2F..%2Fb': 'gs://p/a/./../b',
        'https://a.example/v0/b/p/o/a/../b?alt=media': 'gs://p/a/../b',
        // Not encoded at all, as someone may type it.
        'https://a.example/v0/b/p/o/صورة ١.png': 'gs://p/صورة ١.png',
        // A gs:// URL comes back unchanged.
        'gs://p.appspot.com/a b.txt': 'gs://p.appspot.com/a b.txt',
      };
      cases.forEach((link, gs) => expect(fromArumaUrl(link), gs, reason: link));
    });

    test('refuses names that a reference cannot name', () {
      // firebase_storage's Pointer trims one leading and one trailing '/', and the native SDKs
      // collapse '//': each of these would read back as another object.
      for (final bad in [
        'https://a.example/v0/b/p/o/a%2F%2Fb',
        'https://a.example/v0/b/p/o/%2Fa',
        'https://a.example/v0/b/p/o/a%2F',
        'https://a.example/v0/b/p/o/%2F',
        'https://a.example/v0/b/p/o/e2e%2Fu%2F%2Fx.txt?alt=media&token=T',
        'gs://p/a//b',
        'gs://p//a',
        'gs://p/a/',
      ]) {
        expect(() => fromArumaUrl(bad), throwsA(isA<NotStorageUrlException>()), reason: bad);
      }
    });

    test('refuses what is not a Cloud Storage object URL', () {
      for (final bad in [
        'https://aruma.example.com/firebasestorage.googleapis.com/v0/b/p/o/x', // the prefixed form
        'https://aruma.example.com/v0/b/p/o/',
        'https://aruma.example.com/v0/b//o/x',
        'https://aruma.example.com/v0/b/p',
        'https://aruma.example.com/v0/b/p/o',
        'https://aruma.example.com/v1/b/p/o/x',
        'https://aruma.example.com/v0/b/p/o/%FF', // not UTF-8
        'https://aruma.example.com/v0/b/p/o/%C3', // cut short
        'https://aruma.example.com/v0/b/p/o/x%2', // a bad escape
        'https://aruma.example.com/v0/b/p/o/x%zz',
        'https://aruma.example.com/v0/b/a%2Fb/o/x', // a bucket with '/'
        'https://aruma.example.com?/v0/b/p/o/x',
        'https:///v0/b/p/o/x',
        'ftp://aruma.example.com/v0/b/p/o/x',
        'storage.googleapis.com/p/x',
        'gs://p',
        'gs://p/',
        'gs:///x',
        'not a url',
        '',
      ]) {
        expect(() => fromArumaUrl(bad), throwsA(isA<NotStorageUrlException>()), reason: bad);
      }
    });

    test('is a FormatException with the URL', () {
      try {
        fromArumaUrl('https://aruma.example.com/v0/b/p/o/');
        fail('no exception');
      } on FormatException catch (e) {
        expect(e.source, 'https://aruma.example.com/v0/b/p/o/');
      }
    });

    test('round-trips every name a reference can hold', () {
      // Random names over the characters that need care, encoded as the server encodes them
      // (every byte but the unreserved ones) and as Uri.encodeComponent does.
      const pool = [
        'a',
        'Z',
        '0',
        ' ',
        '+',
        '%',
        '?',
        '#',
        '&',
        '=',
        '/',
        '.',
        '~',
        '*',
        "'",
        'é',
        'ص',
        '١',
        '😀',
        ' ',
        '\u{10ffff}'
      ];
      final rnd = Random(22);
      for (var i = 0; i < 2000; i++) {
        final name =
            List.generate(1 + rnd.nextInt(12), (_) => pool[rnd.nextInt(pool.length)]).join();
        for (final encoded in [_encodeAll(name), Uri.encodeComponent(name)]) {
          final link = 'https://aruma.example.com/v0/b/p.appspot.com/o/$encoded?alt=media&token=T';
          if (name.startsWith('/') || name.endsWith('/') || name.contains('//')) {
            expect(() => fromArumaUrl(link), throwsA(isA<NotStorageUrlException>()), reason: link);
          } else {
            expect(fromArumaUrl(link), 'gs://p.appspot.com/$name', reason: link);
          }
        }
      }
    });
  });
}

/// Percent-encodes every UTF-8 byte except the unreserved characters (RFC 3986).
String _encodeAll(String s) => utf8.encode(s).map((b) {
      final c = String.fromCharCode(b);
      return RegExp(r'^[A-Za-z0-9\-._~]$').hasMatch(c) && b < 0x80
          ? c
          : '%${b.toRadixString(16).toUpperCase().padLeft(2, '0')}';
    }).join();
