// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Handles the Firebase Auth endpoints and the Cloud Storage fallback only (see ``ArumaRedirect``): it performs the rewritten request
/// with its own URLSession and relays the response, data and errors to the original client.
/// The client still sees the original Google URL on the response.
@objc(ArumaRedirectURLProtocol)
public final class ArumaRedirectURLProtocol: URLProtocol {
  /// Set on forwarded requests so they are never handled twice.
  static let handledKey = "dev.aruma.redirect.handled"

  override public class func canInit(with request: URLRequest) -> Bool {
    guard ArumaRedirect.mode != nil, let url = request.url else { return false }
    if URLProtocol.property(forKey: handledKey, in: request) != nil { return false }
    return AuthEndpoints.matches(url) || AuthEndpoints.isStorageHost(url)
  }

  override public class func canonicalRequest(for request: URLRequest) -> URLRequest {
    request
  }

  // State below is touched on the client thread only (startLoading, stopLoading and every relay
  // callback run there), except `forwardedTask`, which the session delegate never reads.
  private var clientThread: Thread?
  private var clientModes: [String] = [RunLoop.Mode.default.rawValue]
  private var stopped = false
  private var forwardedTask: URLSessionDataTask?

  override public func startLoading() {
    clientThread = Thread.current
    if let mode = RunLoop.current.currentMode, mode != .default {
      clientModes.append(mode.rawValue)
    }
    guard let mode = ArumaRedirect.mode, let original = request.url else {
      return fail(URLError(.unsupportedURL))
    }
    switch mode {
    case .blocked(let reason):
      fail(URLError(.cannotConnectToHost, userInfo: [NSLocalizedDescriptionKey: "ArumaRedirect: \(reason)"]))
    case .forward(let base):
      if AuthEndpoints.isStorageHost(original) {
        // Only an official FirebaseStorage sends this: the patched one goes to ArumaURL itself.
        guard AuthEndpoints.storageRedirect else {
          if AuthEndpoints.firstStorageRequest() {
            ArumaRedirect.log("refused a Cloud Storage request to \(AuthEndpoints.storageHost): "
              + "FirebaseStorage is the official build and ArumaStorageRedirect is false. "
              + "Use the patched FirebaseStorage (sdk/ios/firebase-storage-patch)")
          }
          return fail(URLError(.cannotConnectToHost, userInfo: [NSLocalizedDescriptionKey:
              "ArumaRedirect: Cloud Storage requests to Google are refused (ArumaStorageRedirect is false)"]))
        }
        if AuthEndpoints.firstStorageRequest() {
          ArumaRedirect.log("Cloud Storage requests to \(AuthEndpoints.storageHost) are redirected at "
            + "run time: FirebaseStorage is the official build, so its download URLs name Google. "
            + "Use the patched FirebaseStorage (sdk/ios/firebase-storage-patch), or ArumaUrls.toArumaUrl")
        }
      }
      guard let target = AuthEndpoints.rewrite(original, to: base) else {
        return fail(URLError(.unsupportedURL))
      }
      let body: Data?
      do {
        body = try RequestBody.read(from: request)
      } catch {
        return fail(error)
      }
      var forwarded = URLRequest(url: target, cachePolicy: .reloadIgnoringLocalCacheData,
                                 timeoutInterval: request.timeoutInterval)
      forwarded.httpMethod = request.httpMethod ?? "GET"
      forwarded.allHTTPHeaderFields = request.allHTTPHeaderFields?.filter { key, _ in
        let k = key.lowercased()
        return k != "host" && k != "content-length"
      }
      forwarded.httpBody = body
      forwarded.httpShouldHandleCookies = false
      let marked = (forwarded as NSURLRequest).mutableCopy() as! NSMutableURLRequest
      URLProtocol.setProperty(true, forKey: Self.handledKey, in: marked)
      if ArumaRedirect.logRequests {
        ArumaRedirect.log("\(forwarded.httpMethod ?? "GET") \(Self.loggable(original)) -> \(Self.loggable(target))")
      }
      forwardedTask = Forwarder.shared.start(marked as URLRequest, for: self)
    }
  }

  override public func stopLoading() {
    stopped = true
    if let task = forwardedTask {
      Forwarder.shared.forget(task)
      task.cancel()
    }
    forwardedTask = nil
  }

  // MARK: relays from the internal session (any thread) to the client thread

  func relay(response: URLResponse) {
    let mapped = Self.responseWithOriginalURL(response, url: request.url)
    onClient { $0.client?.urlProtocol($0, didReceive: mapped, cacheStoragePolicy: .notAllowed) }
  }

  func relay(data: Data) {
    onClient { $0.client?.urlProtocol($0, didLoad: data) }
  }

  func relay(completion error: Error?) {
    onClient { me in
      if let error = error {
        me.client?.urlProtocol(me, didFailWithError: error)
      } else {
        me.client?.urlProtocolDidFinishLoading(me)
      }
    }
  }

  /// The server redirected: hand the new request to the client, which loads it through the normal
  /// URL loading system (and through this protocol again only if it is an Auth endpoint).
  func relay(redirect response: HTTPURLResponse, to newRequest: URLRequest) {
    let next = (newRequest as NSURLRequest).mutableCopy() as! NSMutableURLRequest
    URLProtocol.removeProperty(forKey: Self.handledKey, in: next)
    let mapped = Self.responseWithOriginalURL(response, url: request.url)
    onClient { me in
      me.client?.urlProtocol(me, wasRedirectedTo: next as URLRequest, redirectResponse: mapped)
      me.client?.urlProtocol(me, didFailWithError: URLError(.cancelled))
    }
  }

  private func fail(_ error: Error) {
    onClient { $0.client?.urlProtocol($0, didFailWithError: error) }
  }

  private func onClient(_ body: @escaping (ArumaRedirectURLProtocol) -> Void) {
    let box = ClientCall { [self] in
      guard !stopped else { return }
      body(self)
    }
    guard let thread = clientThread, thread != Thread.current else { return box.run() }
    perform(#selector(runClientCall(_:)), on: thread, with: box, waitUntilDone: false, modes: clientModes)
  }

  @objc private func runClientCall(_ call: ClientCall) {
    call.run()
  }

  // MARK: helpers

  /// The client asked for the Google URL, so the response carries it. The body was already decoded by
  /// the internal session, so encoding and length headers no longer describe it.
  static func responseWithOriginalURL(_ response: URLResponse, url: URL?) -> URLResponse {
    guard let http = response as? HTTPURLResponse, let url = url else { return response }
    var headers: [String: String] = [:]
    for (key, value) in http.allHeaderFields {
      guard let k = key as? String, let v = value as? String else { continue }
      let lower = k.lowercased()
      if lower == "content-encoding" || lower == "content-length" { continue }
      headers[k] = v
    }
    return HTTPURLResponse(url: url, statusCode: http.statusCode, httpVersion: "HTTP/1.1",
                           headerFields: headers) ?? response
  }

  /// scheme://host[:port]/path — never the query (it holds the API key) or anything else.
  static func loggable(_ url: URL) -> String {
    guard var c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return "?" }
    c.query = nil
    c.fragment = nil
    c.user = nil
    c.password = nil
    return c.string ?? "?"
  }
}

final class ClientCall: NSObject {
  private let body: () -> Void
  init(_ body: @escaping () -> Void) { self.body = body }
  func run() { body() }
}

/// One internal URLSession for every forwarded request. Its configuration never contains
/// ArumaRedirectURLProtocol, so a forwarded request cannot loop back.
final class Forwarder: NSObject, URLSessionDataDelegate {
  static let shared = Forwarder()

  /// Test hook: protocol classes placed first in the internal session (a fake server).
  static var testProtocolClasses: [AnyClass] = []

  private let lock = NSLock()
  private var handlers: [Int: ArumaRedirectURLProtocol] = [:]

  private lazy var session: URLSession = {
    let config = URLSessionConfiguration.ephemeral
    let own = ObjectIdentifier(ArumaRedirectURLProtocol.self)
    config.protocolClasses = Forwarder.testProtocolClasses
      + (config.protocolClasses ?? []).filter { ObjectIdentifier($0) != own }
    config.urlCache = nil
    config.requestCachePolicy = .reloadIgnoringLocalCacheData
    config.httpCookieStorage = nil
    config.httpShouldSetCookies = false
    let queue = OperationQueue()
    queue.name = "dev.aruma.redirect"
    queue.maxConcurrentOperationCount = 1
    return URLSession(configuration: config, delegate: self, delegateQueue: queue)
  }()

  func start(_ request: URLRequest, for handler: ArumaRedirectURLProtocol) -> URLSessionDataTask {
    lock.lock()
    let task = session.dataTask(with: request)
    handlers[task.taskIdentifier] = handler
    lock.unlock()
    task.resume()
    return task
  }

  func forget(_ task: URLSessionTask) {
    lock.lock(); handlers[task.taskIdentifier] = nil; lock.unlock()
  }

  private func handler(for task: URLSessionTask, remove: Bool = false) -> ArumaRedirectURLProtocol? {
    lock.lock(); defer { lock.unlock() }
    return remove ? handlers.removeValue(forKey: task.taskIdentifier) : handlers[task.taskIdentifier]
  }

  func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                  completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
    handler(for: dataTask)?.relay(response: response)
    completionHandler(.allow)
  }

  func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
    handler(for: dataTask)?.relay(data: data)
  }

  func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
    handler(for: task, remove: true)?.relay(completion: error)
  }

  func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                  newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
    if let handler = handler(for: task, remove: true) {
      handler.relay(redirect: response, to: request)
    }
    completionHandler(nil)
    task.cancel()
  }

  func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge,
                  completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
    // Server trust is evaluated by the system: an untrusted certificate fails the request.
    completionHandler(.performDefaultHandling, nil)
  }
}

/// Puts ArumaRedirectURLProtocol first in every session the app (and Firebase) creates.
/// `URLProtocol.registerClass` covers `URLSession.shared` only; sessions built from
/// `URLSessionConfiguration.default` / `.ephemeral` take their protocol list from the configuration.
/// Background sessions ignore custom protocols, so requests sent through them are not rewritten.
enum SessionHook {
  private static let lock = NSLock()
  private static var installed = false

  static func installOnce() {
    lock.lock()
    defer { lock.unlock() }
    guard !installed else { return }
    installed = true
    URLProtocol.registerClass(ArumaRedirectURLProtocol.self)
    swizzle("defaultSessionConfiguration")
    swizzle("ephemeralSessionConfiguration")
  }

  private static func swizzle(_ name: String) {
    let selector = NSSelectorFromString(name)
    guard let method = class_getClassMethod(URLSessionConfiguration.self, selector) else { return }
    typealias Factory = @convention(c) (AnyClass, Selector) -> URLSessionConfiguration
    let original = unsafeBitCast(method_getImplementation(method), to: Factory.self)
    let own = ObjectIdentifier(ArumaRedirectURLProtocol.self)
    let replacement: @convention(block) (AnyClass) -> URLSessionConfiguration = { cls in
      let config = original(cls, selector)
      var classes = config.protocolClasses ?? []
      if !classes.contains(where: { ObjectIdentifier($0) == own }) {
        classes.insert(ArumaRedirectURLProtocol.self, at: 0)
        config.protocolClasses = classes
      }
      return config
    }
    method_setImplementation(method, imp_implementationWithBlock(replacement))
  }
}
