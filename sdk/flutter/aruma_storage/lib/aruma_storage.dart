// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

/// Cloud Storage for Firebase on Aruma, from Flutter: the download URL that
/// the native SDKs return when the app is built for Aruma, read back with
/// `FirebaseStorage.refFromURL`.
library;

export 'src/urls.dart' show NotStorageUrlException, fromArumaUrl;
