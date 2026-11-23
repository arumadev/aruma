import 'dart:async';

import 'package:firebase_core/firebase_core.dart';
import 'package:flutter/foundation.dart';

import '../repositories.dart';

/// Turns a FlutterFire exception into an [AppException] the screens can word.
AppException toAppException(Object error) {
  if (error is AppException) return error;
  if (error is FirebaseException) {
    final kind = switch (error.code) {
      // Email enumeration protection reports a wrong password as invalid-credential.
      'wrong-password' || 'invalid-credential' || 'invalid-login-credentials' => AppErrorKind.wrongPassword,
      'user-not-found' => AppErrorKind.userNotFound,
      'weak-password' => AppErrorKind.weakPassword,
      'email-already-in-use' => AppErrorKind.emailInUse,
      'invalid-email' => AppErrorKind.invalidEmail,
      'too-many-requests' => AppErrorKind.tooManyRequests,
      // Firestore and Storage say permission-denied, Storage also unauthorized; the Realtime
      // Database says permission-denied too.
      'permission-denied' || 'unauthorized' || 'unauthenticated' => AppErrorKind.permissionDenied,
      'network-request-failed' || 'unavailable' => AppErrorKind.network,
      _ => AppErrorKind.unknown,
    };
    return AppException(kind, '${error.plugin}/${error.code}: ${error.message}');
  }
  return AppException(AppErrorKind.unknown, '$error');
}

/// Runs [body] and rethrows its failure as an [AppException].
Future<T> guarded<T>(Future<T> Function() body) async {
  try {
    return await body();
  } catch (e, s) {
    final mapped = toAppException(e);
    if (mapped.kind == AppErrorKind.unknown) debugPrint('$e\n$s');
    throw mapped;
  }
}

/// [stream] with its errors turned into [AppException]s.
Stream<T> guardedStream<T>(Stream<T> stream) => stream.transform(
      StreamTransformer<T, T>.fromHandlers(
        handleError: (error, stack, sink) => sink.addError(toAppException(error), stack),
      ),
    );
