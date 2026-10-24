// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Why a URL is not a Cloud Storage location ArumaUrls can convert.
public enum ArumaUrlError: Error, Equatable, CustomStringConvertible {
  /// Not `gs://<bucket>/<name>` or `http(s)://<host>/v0/b/<bucket>/o/<name>`, including the prefixed
  /// form `<aruma>/firebasestorage.googleapis.com/v0/...`, which no platform reads.
  case notStorageURL(String)
  /// The URL's bucket is not the one the caller expects.
  case bucketMismatch(expected: String, found: String)
  /// ``ArumaUrls/toArumaUrl(_:)`` needs ArumaURL at the root of its host (no path prefix).
  case notConfigured(String)

  public var description: String {
    switch self {
    case .notStorageURL(let url):
      return "not a Cloud Storage URL (gs://<bucket>/<name>, or https://<host>/v0/b/<bucket>/o/<name>): \(url)"
    case .bucketMismatch(let expected, let found):
      return "the URL's bucket \(found) is not \(expected)"
    case .notConfigured(let why):
      return "ArumaUrls.toArumaUrl: \(why)"
    }
  }
}

/// Cloud Storage download URLs between their forms (ADR-022 §16). The unified form, which Aruma
/// serves from the root of its host and the patched FirebaseStorage returns, is
/// `https://<aruma-host>/v0/b/<bucket>/o/<encoded name>?alt=media&token=<uuid>`.
public enum ArumaUrls {
  /// Checks a download URL and turns it into the `gs://<bucket>/<name>` that
  /// `Storage.reference(forURL:)` reads back to the same object.
  ///
  /// `reference(forURL:)` ends the app (`fatalError`) on a URL it cannot parse or on another
  /// bucket. Call this first, with the bucket of the Storage instance, and it throws instead:
  ///
  /// ```swift
  /// let ref = storage.reference(forURL: try ArumaUrls.fromArumaUrl(link, bucket: storage.reference().bucket))
  /// ```
  ///
  /// Any host is accepted (the unified form, Google's with or without `:443`), as on iOS. The name
  /// is percent-decoded and written raw after the bucket, because FirebaseStorage reads a `gs://`
  /// name as it is (`StoragePath.path(GSURI:)`): encoding it would name another object.
  ///
  /// - Throws: ``ArumaUrlError``.
  public static func fromArumaUrl(_ url: String, bucket: String? = nil) throws -> String {
    let (found, name) = try location(of: url)
    if let bucket, !bucket.isEmpty, bucket != found {
      throw ArumaUrlError.bucketMismatch(expected: bucket, found: found)
    }
    return "gs://\(found)/\(name)"
  }

  /// Moves a download URL on Google's host (as the official FirebaseStorage returns it with the
  /// run-time fallback, with `:443`) to the unified form on the installed ArumaURL, so that it can
  /// be shown, stored or shared without reaching Google. Any other URL comes back unchanged.
  ///
  /// - Throws: ``ArumaUrlError/notConfigured(_:)`` when ArumaRedirect is not installed, or its
  ///   ArumaURL has a path: Aruma serves Storage from the root of its host.
  public static func toArumaUrl(_ url: String) throws -> String {
    guard let c = URLComponents(string: url),
          c.scheme?.lowercased() == "https",
          var host = c.host?.lowercased(),
          c.user == nil, c.password == nil,
          c.port == nil || c.port == 443,
          c.percentEncodedPath.hasPrefix("/v0/b/")
    else { return url }
    if host.hasSuffix(".") { host.removeLast() }
    guard host == AuthEndpoints.storageHost else { return url }
    guard case .forward(let base)? = ArumaRedirect.mode else {
      throw ArumaUrlError.notConfigured("ArumaRedirect is not installed (no ArumaURL)")
    }
    guard base.components.percentEncodedPath.isEmpty else {
      throw ArumaUrlError.notConfigured("ArumaURL \(base.url.absoluteString) has a path")
    }
    // The unified form: host in lower case without a trailing dot, no default port.
    var out = URLComponents()
    out.scheme = base.components.scheme
    var arumaHost = base.components.host?.lowercased() ?? ""
    if arumaHost.hasSuffix(".") { arumaHost.removeLast() }
    out.host = arumaHost
    let defaultPort = base.components.scheme == "https" ? 443 : 80
    out.port = base.components.port == defaultPort ? nil : base.components.port
    out.percentEncodedPath = c.percentEncodedPath
    out.percentEncodedQuery = c.percentEncodedQuery
    return out.string ?? url
  }

  /// The bucket and the decoded object name of a `gs://` or download URL.
  ///
  /// FirebaseStorage collapses `//` and trims leading and trailing `/` in a path
  /// (`StoragePath.standardizedPathForString`), so a name with an empty segment would read back as
  /// another object: such names are refused.
  static func location(of url: String) throws -> (bucket: String, name: String) {
    let (bucket, name) = try split(url)
    guard !name.hasPrefix("/"), !name.hasSuffix("/"), !name.contains("//") else {
      throw ArumaUrlError.notStorageURL(url)
    }
    return (bucket, name)
  }

  private static func split(_ url: String) throws -> (bucket: String, name: String) {
    if url.hasPrefix("gs://") {
      let rest = url.dropFirst("gs://".count)
      guard let slash = rest.firstIndex(of: "/"), slash != rest.startIndex,
            rest.index(after: slash) != rest.endIndex
      else { throw ArumaUrlError.notStorageURL(url) }
      return (String(rest[..<slash]), String(rest[rest.index(after: slash)...]))
    }
    guard let c = URLComponents(string: url),
          let scheme = c.scheme?.lowercased(), scheme == "https" || scheme == "http",
          let host = c.host, !host.isEmpty,
          c.percentEncodedPath.hasPrefix("/v0/b/")
    else { throw ArumaUrlError.notStorageURL(url) }
    let rest = c.percentEncodedPath.dropFirst("/v0/b/".count)
    guard let o = rest.range(of: "/o/"), o.lowerBound != rest.startIndex, o.upperBound != rest.endIndex
    else { throw ArumaUrlError.notStorageURL(url) }
    guard let bucket = decode(rest[..<o.lowerBound]), !bucket.contains("/"),
          let name = decode(rest[o.upperBound...]), !name.isEmpty
    else { throw ArumaUrlError.notStorageURL(url) }
    return (bucket, name)
  }

  /// Strict percent-decoding as UTF-8 ('+' stays '+'), or nil on a bad escape.
  private static func decode(_ s: Substring) -> String? {
    var bytes: [UInt8] = []
    bytes.reserveCapacity(s.utf8.count)
    var it = s.utf8.makeIterator()
    while let b = it.next() {
      guard b == UInt8(ascii: "%") else {
        bytes.append(b)
        continue
      }
      guard let hi = it.next().flatMap(hexValue), let lo = it.next().flatMap(hexValue) else { return nil }
      bytes.append(hi << 4 | lo)
    }
    let s = String(decoding: bytes, as: UTF8.self)
    // Invalid UTF-8 was repaired with U+FFFD: refuse it rather than name another object.
    return Array(s.utf8) == bytes ? s : nil
  }

  private static func hexValue(_ c: UInt8) -> UInt8? {
    switch c {
    case UInt8(ascii: "0")...UInt8(ascii: "9"): return c - UInt8(ascii: "0")
    case UInt8(ascii: "a")...UInt8(ascii: "f"): return c - UInt8(ascii: "a") + 10
    case UInt8(ascii: "A")...UInt8(ascii: "F"): return c - UInt8(ascii: "A") + 10
    default: return nil
    }
  }
}
