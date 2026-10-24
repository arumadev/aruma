import 'dart:io';

/// The Dart half of the network guard. Every socket Dart code opens, `dart:io`'s HttpClient
/// included (and so `Image.network`), goes through `Socket.connect` or `Socket.startConnect`,
/// which ask the current IOOverrides. This one lets connections to this device's loopback through
/// and records them, and refuses and records every other one: the native SDKs never see these
/// sockets, so neither does their guard. The other halves are native: iptables on Android, the
/// URLSession guard and a socket watcher on iOS.
final class SocketGuard extends IOOverrides {
  final List<String> loopback = [];
  final List<String> outside = [];

  /// Drops the last n refused connections: the guard's own self-test.
  void forget(int n) => outside.removeRange(outside.length - n, outside.length);

  /// Records the connection, and returns why it is refused, or null.
  SocketException? _check(Object? host, int port) {
    final name = host is InternetAddress ? host.address : '$host';
    final isLoopback = host is InternetAddress
        ? host.isLoopback
        : name == 'localhost' || (InternetAddress.tryParse(name)?.isLoopback ?? false);
    if (isLoopback) {
      loopback.add('$name:$port');
      return null;
    }
    outside.add('$name:$port');
    return SocketException('refused by the e2e network guard: $name:$port');
  }

  @override
  Future<Socket> socketConnect(
    host,
    int port, {
    sourceAddress,
    int sourcePort = 0,
    Duration? timeout,
  }) {
    final refused = _check(host, port);
    if (refused != null) return Future.error(refused);
    return super.socketConnect(
      host,
      port,
      sourceAddress: sourceAddress,
      sourcePort: sourcePort,
      timeout: timeout,
    );
  }

  @override
  Future<ConnectionTask<Socket>> socketStartConnect(
    host,
    int port, {
    sourceAddress,
    int sourcePort = 0,
  }) {
    final refused = _check(host, port);
    if (refused != null) return Future.error(refused);
    return super.socketStartConnect(
      host,
      port,
      sourceAddress: sourceAddress,
      sourcePort: sourcePort,
    );
  }
}
