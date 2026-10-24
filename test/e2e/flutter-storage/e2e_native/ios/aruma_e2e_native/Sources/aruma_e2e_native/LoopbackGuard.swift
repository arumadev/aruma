import ArumaRedirect
import Foundation

/// The URLSession half of the network guard on iOS (as test/e2e/ios-storage's): every URLSession
/// request in the process passes through it first.
/// - To this machine (the Aruma server): allowed and recorded.
/// - A Firebase request to Google that ArumaRedirect takes: recorded and left to ArumaRedirect,
///   which sends it to Aruma from its own session. Here that is Auth only: a Storage request would
///   be refused (ArumaStorageRedirect is false), and the run fails on any.
/// - Anything else: refused (not connected to the internet) and recorded, and the run fails.
///
/// Dart's sockets never reach it (lib/guard.dart is their half), and run.sh samples the app's
/// sockets as a second look.
final class LoopbackGuard: URLProtocol {
  private static let loopback: Set<String> = ["localhost", "127.0.0.1", "::1"]
  private static let lock = NSLock()
  private static var log = (loopback: [String](), redirected: [String](), blocked: [String]())

  static var loopbackPaths: [String] { lock.lock(); defer { lock.unlock() }; return log.loopback }
  static var redirected: [String] { lock.lock(); defer { lock.unlock() }; return log.redirected }
  static var blocked: [String] { lock.lock(); defer { lock.unlock() }; return log.blocked }

  /// Drops the last n refused requests: the guard's own self-test.
  static func forget(_ n: Int) {
    lock.lock(); defer { lock.unlock() }
    log.blocked.removeLast(n)
  }

  override class func canInit(with request: URLRequest) -> Bool {
    guard let url = request.url, let host = url.host?.lowercased() else { return false }
    lock.lock(); defer { lock.unlock() }
    if loopback.contains(host) {
      log.loopback.append("\(request.httpMethod ?? "GET") \(host)\(url.path)")
      return false
    }
    if ArumaRedirectURLProtocol.canInit(with: request) {
      log.redirected.append(host + url.path)
      return false
    }
    // Never the query: it may hold the API key.
    log.blocked.append("\(url.scheme ?? "?")://\(host)\(url.path)")
    return true
  }

  override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

  override func startLoading() {
    client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
  }

  override func stopLoading() {}

  private static var installed = false

  /// Registers the guard, and puts it first in the default and ephemeral configurations, where
  /// Firebase's sessions come from (ArumaRedirect put itself there before main; this goes before
  /// it). URLProtocol.registerClass alone only reaches URLSession.shared.
  static func install() {
    guard !installed else { return }
    installed = true
    URLProtocol.registerClass(LoopbackGuard.self)
    for name in ["defaultSessionConfiguration", "ephemeralSessionConfiguration"] {
      let selector = NSSelectorFromString(name)
      let original = class_getClassMethod(URLSessionConfiguration.self, selector)!
      typealias Factory = @convention(c) (AnyClass, Selector) -> URLSessionConfiguration
      let make = unsafeBitCast(method_getImplementation(original), to: Factory.self)
      let block: @convention(block) (AnyClass) -> URLSessionConfiguration = { _ in
        let config = make(URLSessionConfiguration.self, selector)
        config.protocolClasses = [LoopbackGuard.self] + (config.protocolClasses ?? [])
        return config
      }
      method_setImplementation(original, imp_implementationWithBlock(block))
    }
  }
}
