// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Why an Aruma URL was refused.
public enum ArumaRedirectError: Error, Equatable, CustomStringConvertible {
  /// Not an absolute http(s) URL with a host, or it carries a query, fragment or credentials.
  case invalidURL(String)
  /// `http` to a host that is not loopback, without the explicit insecure flag.
  case insecureURL(String)
  /// The app links the official FirebaseStorage while ArumaStorageRedirect is false.
  case officialFirebaseStorage(String)

  public var description: String {
    switch self {
    case .invalidURL(let url):
      return "ArumaURL \"\(url)\" is not an absolute http(s) URL with a host and no query"
    case .insecureURL(let url):
      return "ArumaURL \"\(url)\" uses http to a non-loopback host; use https, or set ArumaAllowInsecureURL for development"
    case .officialFirebaseStorage(let reason):
      return reason
    }
  }
}

/// The Aruma server requests are sent to: `scheme://host[:port][/prefix]`, without a trailing slash.
struct ArumaBase: Equatable {
  let components: URLComponents

  init(_ raw: String, allowInsecure: Bool) throws {
    let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    guard var c = URLComponents(string: trimmed),
          let scheme = c.scheme?.lowercased(), scheme == "https" || scheme == "http",
          let host = c.host, !host.isEmpty,
          c.query == nil, c.fragment == nil, c.user == nil, c.password == nil
    else { throw ArumaRedirectError.invalidURL(trimmed) }
    if scheme == "http" && !allowInsecure && !ArumaBase.isLoopback(host) {
      throw ArumaRedirectError.insecureURL(trimmed)
    }
    c.scheme = scheme
    while c.percentEncodedPath.hasSuffix("/") { c.percentEncodedPath.removeLast() }
    components = c
  }

  var url: URL { components.url! }

  /// `localhost`, `127.0.0.0/8` and `::1`.
  static func isLoopback(_ host: String) -> Bool {
    let h = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
    if h == "localhost" || h == "::1" { return true }
    let octets = h.split(separator: ".", omittingEmptySubsequences: false)
    guard octets.count == 4, octets[0] == "127" else { return false }
    return octets.allSatisfy { part in
      !part.isEmpty && part.count <= 3 && part.allSatisfy(\.isASCII) && (UInt8(part) != nil)
    }
  }
}

/// The Firebase endpoint families ArumaRedirect handles, and how each maps onto an Aruma server:
/// the three of Auth (ADR-014), and Cloud Storage for Firebase as the run-time fallback of
/// ADR-022 §16.3. Aruma serves Google's paths under its own host, so a rewrite only adds the
/// original host as the first path segment.
enum AuthEndpoints {
  static let identityToolkitHost = "identitytoolkit.googleapis.com"
  static let secureTokenHost = "securetoken.googleapis.com"
  static let legacyHost = "www.googleapis.com"
  static let legacyPathPrefix = "/identitytoolkit/v3/relyingparty/"
  /// Cloud Storage for Firebase. The production path is the patched FirebaseStorage
  /// (sdk/ios/firebase-storage-patch), which sends nothing to this host when ArumaURL is set, so
  /// ArumaRedirect only sees what an official FirebaseStorage sends.
  static let storageHost = "firebasestorage.googleapis.com"
  private static let storageLock = NSLock()
  private static var _storageRedirect = true
  private static var _storageSeen = false

  /// False (Info.plist `ArumaStorageRedirect`): a Storage request is refused instead of rewritten.
  static var storageRedirect: Bool {
    get { storageLock.lock(); defer { storageLock.unlock() }; return _storageRedirect }
    set { storageLock.lock(); _storageRedirect = newValue; storageLock.unlock() }
  }

  /// True the first time only, so that the official-build notice is logged once.
  static func firstStorageRequest() -> Bool {
    storageLock.lock(); defer { storageLock.unlock() }
    if _storageSeen { return false }
    _storageSeen = true
    return true
  }

  /// Test hook.
  static func resetStorage() {
    storageLock.lock(); _storageRedirect = true; _storageSeen = false; storageLock.unlock()
  }

  /// True for a production request to one of the families: https on the default port.
  static func matches(_ url: URL) -> Bool {
    target(of: url) != nil
  }

  /// True for any request to Cloud Storage's host, whatever its scheme, port or path. ArumaRedirect
  /// takes all of them: the ones ``rewrite(_:to:)`` cannot map (dot segments, another port, http)
  /// fail instead of reaching Google.
  static func isStorageHost(_ url: URL) -> Bool {
    guard var host = url.host?.lowercased() else { return false }
    if host.hasSuffix(".") { host.removeLast() }
    return host == storageHost
  }

  /// `https://<google host><path>?<query>` → `<base>/<google host><path>?<query>`, or nil when
  /// the URL is not one of the families (it is then passed through untouched).
  static func rewrite(_ url: URL, to base: ArumaBase) -> URL? {
    guard let (host, path, query) = target(of: url) else { return nil }
    var out = base.components
    out.percentEncodedPath = base.components.percentEncodedPath + "/" + host + path
    out.percentEncodedQuery = query
    return out.url
  }

  private static func target(of url: URL) -> (host: String, path: String, query: String?)? {
    guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false),
          c.scheme?.lowercased() == "https",
          let host = c.host?.lowercased(),
          c.port == nil || c.port == 443,
          c.user == nil, c.password == nil
    else { return nil }
    let path = c.percentEncodedPath
    // Dot segments would let the rewritten path escape the host prefix on the server.
    if path.split(separator: "/").contains(where: { $0 == "." || $0 == ".." }) { return nil }
    switch host {
    case identityToolkitHost, secureTokenHost, storageHost:
      break
    case legacyHost:
      guard path.hasPrefix(legacyPathPrefix) else { return nil }
    default:
      return nil
    }
    return (host, path, c.percentEncodedQuery)
  }
}

/// Request bodies: URLProtocol usually receives them as `httpBodyStream`, not `httpBody`.
enum RequestBody {
  /// Upper bound for a buffered body. Auth bodies are a few kilobytes; a Storage upload through
  /// the fallback above this size fails (the patched FirebaseStorage never comes here).
  static let limit = 16 << 20

  static func read(from request: URLRequest, limit: Int = RequestBody.limit) throws -> Data? {
    if let body = request.httpBody { return body }
    guard let stream = request.httpBodyStream else { return nil }
    return try read(stream, limit: limit)
  }

  static func read(_ stream: InputStream, limit: Int = RequestBody.limit) throws -> Data {
    stream.open()
    defer { stream.close() }
    var data = Data()
    var buffer = [UInt8](repeating: 0, count: 16 * 1024)
    while true {
      let n = stream.read(&buffer, maxLength: buffer.count)
      if n < 0 { throw stream.streamError ?? URLError(.cannotDecodeRawData) }
      if n == 0 { break }
      data.append(buffer, count: n)
      if data.count > limit { throw URLError(.dataLengthExceedsMaximum) }
    }
    return data
  }
}
