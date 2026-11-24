import 'dart:convert';

/// The one Aruma setting of the app, read from `aruma.json` at the root of the project (a Flutter
/// asset). The Android build (android/app/build.gradle.kts), the iOS build (ios/aruma_url.sh) and
/// the web build step (tool/web.sh) read the same file, so the URL is written in one place.
///
/// Everything else (project, API key, app IDs, the Realtime Database URL, the bucket) comes from
/// `lib/firebase_options.dart`, which Aruma generates, or FlutterFire on Firebase.
class AppConfig {
  const AppConfig({required this.useAruma, this.arumaUrl});

  /// Off: the app is a plain Firebase app. No Aruma line runs, and Android and iOS leave the Aruma
  /// libraries out of the build.
  final bool useAruma;

  /// The root of the Aruma server, such as `https://aruma.example.com`. Set when [useAruma] is on.
  final Uri? arumaUrl;

  /// Parses the file. A bad file is an error at start-up, not a silent fall-back to Google.
  factory AppConfig.parse(String source) {
    final Object? json = jsonDecode(source);
    if (json is! Map<String, Object?>) {
      throw const FormatException('aruma.json must hold a JSON object');
    }
    final useAruma = json['useAruma'];
    if (useAruma is! bool) {
      throw const FormatException('aruma.json: useAruma must be true or false');
    }
    if (!useAruma) return const AppConfig(useAruma: false);
    final raw = json['arumaUrl'];
    if (raw is! String || raw.trim().isEmpty) {
      throw const FormatException('aruma.json: arumaUrl is missing');
    }
    final url = Uri.tryParse(raw.trim());
    if (url == null ||
        (url.scheme != 'https' && url.scheme != 'http') ||
        url.host.isEmpty ||
        (url.path.isNotEmpty && url.path != '/') ||
        url.hasQuery ||
        url.hasFragment) {
      throw FormatException('aruma.json: arumaUrl must be http(s)://host[:port] with no path', raw);
    }
    return AppConfig(useAruma: true, arumaUrl: url.replace(path: ''));
  }

  /// `host:port` for Firestore's settings: the SDKs speak gRPC (mobile) or WebChannel (web) to it.
  String get firestoreHost {
    final url = arumaUrl!;
    return url.hasPort ? '${url.host}:${url.port}' : url.host;
  }

  /// Firestore's `sslEnabled`: false only for an http Aruma (development on your own machine).
  bool get firestoreSsl => arumaUrl!.scheme == 'https';
}
