// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Which FirebaseStorage the app links (ADR-022 §16.3), as ArumaRedirect checks it at install.
///
/// A SwiftPM graph can silently resolve the official firebase-ios-sdk instead of the mirror (a
/// missing `mirrors.json`, or the mirror URL named next to the official one): resolution succeeds
/// and Storage still works through the run-time fallback, but `downloadURL()` names Google. The
/// patched build carries the Objective-C class `FIRArumaStorageBuild`; the official one does not.
public enum FirebaseStorageBuild: String, Equatable {
  /// No FirebaseStorage in the process (no `FIRStorage` class).
  case absent
  /// The patched FirebaseStorage: it reads ArumaURL itself, and its download URLs are on Aruma.
  case patched
  /// The official FirebaseStorage: requests reach Aruma only through the fallback, and download
  /// URLs name Google.
  case official
}

enum StorageBuildCheck {
  static let storageClass = "FIRStorage"
  static let markerClass = "FIRArumaStorageBuild"

  static func build(lookup: (String) -> AnyClass? = { NSClassFromString($0) }) -> FirebaseStorageBuild {
    guard lookup(storageClass) != nil else { return .absent }
    return lookup(markerClass) != nil ? .patched : .official
  }

  /// What install does about `build`: the lines to log, and the refusal when the patch is
  /// mandatory (ArumaStorageRedirect false) and the build is the official one.
  static func verdict(_ build: FirebaseStorageBuild, storageRedirect: Bool) -> (log: [String], refusal: String?) {
    switch build {
    case .absent:
      return ([], nil)
    case .patched:
      return (["FirebaseStorage is the Aruma build: it talks to ArumaURL, and its download URLs are on Aruma"], nil)
    case .official:
      let banner = "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"
      let lines = [
        banner,
        "WARNING: FirebaseStorage is the OFFICIAL build, not the Aruma mirror.",
        "Its download URLs name firebasestorage.googleapis.com, and anything outside this app's",
        "URLSessions (AVPlayer, WKWebView, a shared link) sends them to Google.",
        "SwiftPM resolved the official firebase-ios-sdk: check mirrors.json (swift package config",
        "set-mirror), and that nothing names the mirror URL directly (sdk/ios/firebase-storage-patch).",
        storageRedirect
          ? "Storage requests are redirected at run time (ArumaStorageRedirect is not false)."
          : "ArumaStorageRedirect is false, so the patched build is mandatory: refusing to start.",
        banner,
      ]
      return (lines, storageRedirect ? nil : "FirebaseStorage is the official build and ArumaStorageRedirect is false: "
        + "the app must be built with the Aruma mirror of firebase-ios-sdk (sdk/ios/firebase-storage-patch)")
    }
  }
}
