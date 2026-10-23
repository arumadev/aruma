// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Why an Aruma URL was refused.
public enum ArumaRedirectError: Error, Equatable, CustomStringConvertible {
  /// Not an absolute http(s) URL with a host, or it carries a query, fragment or credentials.
  case invalidURL(String)
  /// `http` to a host that is not loopback, without the explicit insecure flag.
  case insecureURL(String)

  public var description: String {
    switch self {
    case .invalidURL(let url):
      return "ArumaURL \"\(url)\" is not an absolute http(s) URL with a host and no query"
    case .insecureURL(let url):
      return "ArumaURL \"\(url)\" uses http to a non-loopback host; use https, or set ArumaAllowInsecureURL for development"
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

/// The three Firebase Auth endpoint families, and how each maps onto an Aruma server (ADR-014).
enum AuthEndpoints {
  static let identityToolkitHost = "identitytoolkit.googleapis.com"
  static let secureTokenHost = "securetoken.googleapis.com"
  static let legacyHost = "www.googleapis.com"
  static let legacyPathPrefix = "/identitytoolkit/v3/relyingparty/"
  /// SPIKE (S0-B, docs/research/storage-production-path.md): Cloud Storage for Firebase, off by default.
  static let storageHost = "firebasestorage.googleapis.com"
  private static let spikeLock = NSLock()
  private static var _storageSpike = false
  static var storageSpike: Bool {
    get { spikeLock.lock(); defer { spikeLock.unlock() }; return _storageSpike }
    set { spikeLock.lock(); _storageSpike = newValue; spikeLock.unlock() }
  }

  /// True for a production Auth request: https on the default port to one of the three families.
  static func matches(_ url: URL) -> Bool {
    target(of: url) != nil
  }

  /// `https://<google host><path>?<query>` → `<base>/<google host><path>?<query>`, or nil when
  /// the URL is not an Auth endpoint (it is then passed through untouched).
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
    case identityToolkitHost, secureTokenHost:
      break
    case legacyHost:
      guard path.hasPrefix(legacyPathPrefix) else { return nil }
    case storageHost:
      guard storageSpike else { return nil }
    default:
      return nil
    }
    return (host, path, c.percentEncodedQuery)
  }
}

/// Request bodies: URLProtocol usually receives them as `httpBodyStream`, not `httpBody`.
enum RequestBody {
  /// Upper bound for a buffered body. Auth bodies are a few kilobytes.
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
