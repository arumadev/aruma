// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import XCTest
@testable import ArumaRedirect

/// Stands in for the Aruma server: placed first in the internal session (and in the client session for
/// redirects), it serves `https://fake.aruma.test` without any network.
final class FakeAruma: URLProtocol {
  struct Seen { let url: URL; let method: String; let headers: [String: String]; let body: Data }

  static let host = "fake.aruma.test"
  private static let lock = NSLock()
  private static var _seen: [Seen] = []
  private static var _stopped = 0

  static var seen: [Seen] { lock.lock(); defer { lock.unlock() }; return _seen }
  static var stopped: Int { lock.lock(); defer { lock.unlock() }; return _stopped }
  static func reset() { lock.lock(); _seen = []; _stopped = 0; lock.unlock() }

  override class func canInit(with request: URLRequest) -> Bool { request.url?.host == host }
  override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

  override func startLoading() {
    let url = request.url!
    let body = (try? RequestBody.read(from: request)) ?? nil
    FakeAruma.lock.lock()
    FakeAruma._seen.append(Seen(url: url, method: request.httpMethod ?? "GET",
                                headers: request.allHTTPHeaderFields ?? [:], body: body ?? Data()))
    FakeAruma.lock.unlock()
    let path = url.path
    if path.hasSuffix("/slow") { return }  // never answers: timeouts and cancellation
    if path.hasSuffix("/broken") { return client!.urlProtocol(self, didFailWithError: URLError(.networkConnectionLost)) }
    if path.hasSuffix("/moved") {
      let to = URL(string: "https://\(FakeAruma.host)/landing")!
      let r = HTTPURLResponse(url: url, statusCode: 302, httpVersion: "HTTP/1.1", headerFields: ["Location": to.absoluteString])!
      client!.urlProtocol(self, wasRedirectedTo: URLRequest(url: to), redirectResponse: r)
      return client!.urlProtocol(self, didFailWithError: URLError(.cancelled))
    }
    let status = path.hasSuffix("/error") ? 400 : 200
    let json = try! JSONSerialization.data(withJSONObject: ["path": path, "query": url.query ?? "", "bytes": body?.count ?? 0])
    let r = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1",
                            headerFields: ["Content-Type": "application/json", "X-Aruma": "1"])!
    client!.urlProtocol(self, didReceive: r, cacheStoragePolicy: .notAllowed)
    client!.urlProtocol(self, didLoad: json.prefix(5))
    client!.urlProtocol(self, didLoad: json.dropFirst(5))
    client!.urlProtocolDidFinishLoading(self)
  }

  override func stopLoading() {
    FakeAruma.lock.lock(); FakeAruma._stopped += 1; FakeAruma.lock.unlock()
  }
}

final class ForwardingTests: XCTestCase {
  override class func setUp() {
    super.setUp()
    Forwarder.testProtocolClasses = [FakeAruma.self]
  }

  override func setUp() {
    super.setUp()
    FakeAruma.reset()
    try! ArumaRedirect.install(urlString: "https://fake.aruma.test")
  }

  override func tearDown() {
    ArumaRedirect.reset()
    super.tearDown()
  }

  /// A session built the way Firebase builds one: from the (hooked) ephemeral configuration.
  private func session() -> URLSession {
    let config = URLSessionConfiguration.ephemeral
    XCTAssertTrue(config.protocolClasses?.first == ArumaRedirectURLProtocol.self)
    // FakeAruma right after the redirect protocol, ahead of the system HTTP protocol, so a redirect to
    // the fake server never touches the network.
    var classes = config.protocolClasses ?? []
    classes.insert(FakeAruma.self, at: 1)
    config.protocolClasses = classes
    return URLSession(configuration: config)
  }

  private func load(_ request: URLRequest, upload: Data? = nil, cancelAfter: TimeInterval? = nil)
    -> (Data?, HTTPURLResponse?, Error?) {
    let done = expectation(description: "load")
    var result: (Data?, HTTPURLResponse?, Error?) = (nil, nil, nil)
    let handler = { (d: Data?, r: URLResponse?, e: Error?) in
      result = (d, r as? HTTPURLResponse, e)
      done.fulfill()
    }
    let task = upload.map { session().uploadTask(with: request, from: $0, completionHandler: handler) }
      ?? session().dataTask(with: request, completionHandler: handler)
    task.resume()
    if let delay = cancelAfter {
      DispatchQueue.global().asyncAfter(deadline: .now() + delay) { task.cancel() }
    }
    wait(for: [done], timeout: 10)
    return result
  }

  private func json(_ data: Data?) -> [String: Any] {
    (data.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
  }

  func testPostWithStreamedBodyIsForwarded() throws {
    let original = URL(string: "https://www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K")!
    var request = URLRequest(url: original)
    request.httpMethod = "POST"
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.setValue("ios/12.19.0", forHTTPHeaderField: "X-Client-Version")
    let payload = Data(repeating: 0x41, count: 200_000)
    request.httpBodyStream = InputStream(data: payload)

    let (data, response, error) = load(request)
    XCTAssertNil(error)
    XCTAssertEqual(response?.statusCode, 200)
    XCTAssertEqual(response?.url, original, "the client keeps seeing the Google URL")
    XCTAssertEqual(response?.value(forHTTPHeaderField: "X-Aruma"), "1")
    XCTAssertEqual(json(data)["path"] as? String, "/www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword")
    XCTAssertEqual(json(data)["query"] as? String, "key=K")

    let seen = try XCTUnwrap(FakeAruma.seen.first)
    XCTAssertEqual(FakeAruma.seen.count, 1)
    XCTAssertEqual(seen.url.absoluteString,
                   "https://fake.aruma.test/www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K")
    XCTAssertEqual(seen.method, "POST")
    XCTAssertEqual(seen.headers["X-Client-Version"], "ios/12.19.0")
    XCTAssertEqual(seen.headers["Content-Type"], "application/json")
    XCTAssertEqual(seen.body, payload)
  }

  func testUploadTaskBody() throws {
    var request = URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token?key=K")!)
    request.httpMethod = "POST"
    let payload = Data("grant_type=refresh_token&refresh_token=R".utf8)
    let (data, response, error) = load(request, upload: payload)
    XCTAssertNil(error)
    XCTAssertEqual(response?.statusCode, 200)
    XCTAssertEqual(json(data)["bytes"] as? Int, payload.count)
    XCTAssertEqual(FakeAruma.seen.first?.body, payload)
  }

  func testHTTPErrorStatusIsRelayed() {
    let (data, response, error) = load(URLRequest(url: URL(string: "https://identitytoolkit.googleapis.com/v2/error")!))
    XCTAssertNil(error)
    XCTAssertEqual(response?.statusCode, 400)
    XCTAssertEqual(json(data)["path"] as? String, "/identitytoolkit.googleapis.com/v2/error")
  }

  func testTransportErrorIsRelayed() {
    let (_, _, error) = load(URLRequest(url: URL(string: "https://identitytoolkit.googleapis.com/v2/broken")!))
    XCTAssertEqual((error as? URLError)?.code, .networkConnectionLost)
  }

  func testTimeoutCancelsTheForwardedRequest() {
    var request = URLRequest(url: URL(string: "https://identitytoolkit.googleapis.com/v2/slow")!)
    request.timeoutInterval = 1
    let (_, _, error) = load(request)
    XCTAssertEqual((error as? URLError)?.code, .timedOut)
    waitUntil { FakeAruma.stopped == 1 }
  }

  func testCancellationReachesTheForwardedRequest() {
    let (_, _, error) = load(URLRequest(url: URL(string: "https://identitytoolkit.googleapis.com/v2/slow")!),
                             cancelAfter: 0.3)
    XCTAssertEqual((error as? URLError)?.code, .cancelled)
    waitUntil { FakeAruma.stopped == 1 }
  }

  func testRedirectIsHandedToTheClient() {
    let (data, response, error) = load(URLRequest(url: URL(string: "https://identitytoolkit.googleapis.com/v2/moved")!))
    XCTAssertNil(error)
    XCTAssertEqual(response?.url?.absoluteString, "https://fake.aruma.test/landing")
    XCTAssertEqual(json(data)["path"] as? String, "/landing")
  }

  func testOtherHostsPassThrough() {
    XCTAssertFalse(ArumaRedirectURLProtocol.canInit(with: URLRequest(url: URL(string: "https://example.com/x")!)))
    XCTAssertFalse(ArumaRedirectURLProtocol.canInit(
      with: URLRequest(url: URL(string: "https://firebaseinstallations.googleapis.com/v1/x")!)))
    XCTAssertTrue(ArumaRedirectURLProtocol.canInit(
      with: URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!)))
  }

  func testRefusedURLBlocksInsteadOfReachingGoogle() throws {
    let bundle = try makeBundle(["ArumaURL": "http://aruma.example.com"])
    ArumaRedirect.installFromInfoPlist(bundle)
    XCTAssertNil(ArumaRedirect.baseURL)
    let (_, _, error) = load(URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!))
    XCTAssertEqual((error as? URLError)?.code, .cannotConnectToHost)
    XCTAssertTrue(FakeAruma.seen.isEmpty)
  }

  func testInfoPlist() throws {
    ArumaRedirect.reset()
    ArumaRedirect.installFromInfoPlist(try makeBundle([:]))
    XCTAssertNil(ArumaRedirect.mode, "no ArumaURL: nothing happens")
    XCTAssertFalse(ArumaRedirectURLProtocol.canInit(
      with: URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!)))

    ArumaRedirect.installFromInfoPlist(try makeBundle(["ArumaURL": "  https://aruma.example.com/ ", "ArumaRedirectLog": true]))
    XCTAssertEqual(ArumaRedirect.baseURL?.absoluteString, "https://aruma.example.com")
    XCTAssertTrue(ArumaRedirect.logRequests)

    ArumaRedirect.installFromInfoPlist(try makeBundle(["ArumaURL": "http://aruma.lan:8092", "ArumaAllowInsecureURL": "YES"]))
    XCTAssertEqual(ArumaRedirect.baseURL?.absoluteString, "http://aruma.lan:8092")
  }

  /// The fallback for an official FirebaseStorage, on by default: the prefixed path on Aruma, with
  /// the explicit `:443` the official SDK writes.
  func testStorageIsRedirectedByDefault() throws {
    let original = URL(string: "https://firebasestorage.googleapis.com:443/v0/b/p.appspot.com/o/a%2Fb.txt?alt=media&token=T")!
    let (data, response, error) = load(URLRequest(url: original))
    XCTAssertNil(error)
    XCTAssertEqual(response?.statusCode, 200)
    XCTAssertEqual(response?.url, original)
    XCTAssertEqual(json(data)["path"] as? String, "/firebasestorage.googleapis.com/v0/b/p.appspot.com/o/a/b.txt")
    XCTAssertEqual(FakeAruma.seen.last?.url.absoluteString,
                   "https://fake.aruma.test/firebasestorage.googleapis.com/v0/b/p.appspot.com/o/a%2Fb.txt?alt=media&token=T")
  }

  /// ArumaStorageRedirect false: the patch is mandatory, and an official FirebaseStorage's request
  /// fails instead of reaching Google or Aruma.
  func testStorageRefusedWhenTheRedirectIsOff() throws {
    ArumaRedirect.reset()
    ArumaRedirect.installFromInfoPlist(try makeBundle(["ArumaURL": "https://fake.aruma.test", "ArumaStorageRedirect": false]))
    XCTAssertFalse(ArumaRedirect.storageRedirect)
    let storage = URL(string: "https://firebasestorage.googleapis.com/v0/b/p.appspot.com/o/x")!
    XCTAssertTrue(ArumaRedirectURLProtocol.canInit(with: URLRequest(url: storage)))
    let (_, _, error) = load(URLRequest(url: storage))
    XCTAssertEqual((error as? URLError)?.code, .cannotConnectToHost)
    XCTAssertTrue(FakeAruma.seen.isEmpty)
    // Auth still goes to Aruma.
    let (_, response, authError) = load(URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!))
    XCTAssertNil(authError)
    XCTAssertEqual(response?.statusCode, 200)
  }

  /// A Storage request that cannot be rewritten (dot segments, http, another port, a trailing-dot
  /// host) fails in either mode instead of passing through to Google.
  func testUnmappableStorageRequestsFail() throws {
    for storageRedirect in [true, false] {
      ArumaRedirect.storageRedirect = storageRedirect
      for url in ["https://firebasestorage.googleapis.com/v0/b/p/o/../../x",
                  "http://firebasestorage.googleapis.com/v0/b/p/o/x",
                  "https://firebasestorage.googleapis.com:8443/v0/b/p/o/x",
                  "https://firebasestorage.googleapis.com./v0/b/p/o/x"] {
        XCTAssertTrue(ArumaRedirectURLProtocol.canInit(with: URLRequest(url: URL(string: url)!)), url)
        let (_, _, error) = load(URLRequest(url: URL(string: url)!))
        XCTAssertNotNil(error as? URLError, url)
      }
    }
    XCTAssertTrue(FakeAruma.seen.isEmpty)
  }

  /// The patched FirebaseStorage is told apart from the official one at install.
  func testFirebaseStorageBuildAtInstall() throws {
    final class Marker: NSObject {}
    let present: [String: AnyClass] = ["FIRStorage": Marker.self, "FIRArumaStorageBuild": Marker.self]
    for (classes, build) in [([:], FirebaseStorageBuild.absent),
                             (present, .patched),
                             (present.filter { $0.key == "FIRStorage" }, .official)] as [([String: AnyClass], FirebaseStorageBuild)] {
      ArumaRedirect.reset()
      ArumaRedirect.classLookup = { classes[$0] }
      try ArumaRedirect.install(urlString: "https://fake.aruma.test")
      XCTAssertEqual(ArumaRedirect.firebaseStorageBuild, build)
      XCTAssertNotNil(ArumaRedirect.baseURL, "the official build with the fallback on still installs")
    }
    XCTAssertEqual(StorageBuildCheck.build(), .absent, "no FirebaseStorage in this test process")
  }

  /// ArumaStorageRedirect=false makes the patch mandatory: the official build is refused at
  /// launch (the app stops), and nothing reaches Google or Aruma meanwhile.
  func testOfficialFirebaseStorageIsRefusedWhenThePatchIsMandatory() throws {
    final class Marker: NSObject {}
    ArumaRedirect.reset()
    ArumaRedirect.classLookup = { $0 == "FIRStorage" ? Marker.self : nil }
    var stopped: [String] = []
    ArumaRedirect.stop = { stopped.append($0) }
    ArumaRedirect.installFromInfoPlist(try makeBundle(["ArumaURL": "https://fake.aruma.test", "ArumaStorageRedirect": false]))
    XCTAssertEqual(stopped.count, 1)
    XCTAssertTrue(stopped.first?.contains("official build") ?? false)
    XCTAssertEqual(ArumaRedirect.firebaseStorageBuild, .official)
    XCTAssertNil(ArumaRedirect.baseURL)
    let (_, _, authError) = load(URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!))
    XCTAssertEqual((authError as? URLError)?.code, .cannotConnectToHost)
    XCTAssertTrue(FakeAruma.seen.isEmpty)

    // In code, install throws instead.
    ArumaRedirect.reset()
    ArumaRedirect.classLookup = { $0 == "FIRStorage" ? Marker.self : nil }
    ArumaRedirect.storageRedirect = false
    XCTAssertThrowsError(try ArumaRedirect.install(urlString: "https://fake.aruma.test")) {
      guard case .officialFirebaseStorage? = $0 as? ArumaRedirectError else { return XCTFail("\($0)") }
    }
    // The patched build with the patch mandatory installs.
    ArumaRedirect.classLookup = { _ in Marker.self }
    try ArumaRedirect.install(urlString: "https://fake.aruma.test")
    XCTAssertEqual(ArumaRedirect.firebaseStorageBuild, .patched)
    XCTAssertNotNil(ArumaRedirect.baseURL)
  }

  /// A refused ArumaURL blocks Storage too.
  func testRefusedURLBlocksStorage() throws {
    ArumaRedirect.installFromInfoPlist(try makeBundle(["ArumaURL": "ftp://aruma.example.com"]))
    let (_, _, error) = load(URLRequest(url: URL(string: "https://firebasestorage.googleapis.com/v0/b/p/o/x")!))
    XCTAssertEqual((error as? URLError)?.code, .cannotConnectToHost)
    XCTAssertTrue(FakeAruma.seen.isEmpty)
  }

  func testLoaderRanAtLoadTime() {
    XCTAssertTrue(ArumaRedirect.loaderRan, "the Objective-C +load ran without any call from the test")
  }

  private func waitUntil(_ condition: @escaping () -> Bool) {
    let deadline = Date().addingTimeInterval(5)
    while !condition() && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.05)) }
    XCTAssertTrue(condition())
  }

  private func makeBundle(_ info: [String: Any]) throws -> Bundle {
    let dir = FileManager.default.temporaryDirectory
      .appendingPathComponent("ArumaRedirectTests-\(UUID().uuidString).bundle")
    let contents = dir.appendingPathComponent("Contents")
    try FileManager.default.createDirectory(at: contents, withIntermediateDirectories: true)
    var plist = info
    plist["CFBundleIdentifier"] = "dev.aruma.redirect.tests.\(UUID().uuidString)"
    let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
    try data.write(to: contents.appendingPathComponent("Info.plist"))
    addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
    return try XCTUnwrap(Bundle(url: dir))
  }
}
