import ArumaRedirect
import FirebaseCore
import FirebaseStorage
import GTMSessionFetcherCore
import XCTest

/// S0-B on iOS: one mode per test, each run by ../ios.sh in its own xcodebuild invocation (a fresh
/// process). Every operation is recorded, not asserted; the results are printed as one line,
/// `S0PATH_RESULT {json}`, which ios.sh collects from the log.
///
/// - testEmulator: `useEmulator(withHost: "127.0.0.1", port:)`.
/// - testProdAsIs: production mode, ArumaRedirect as it is (Auth hosts only).
/// - testProdSpike / testProdSpikeHTTPS: production mode, ArumaRedirect with the storage spike.
/// - testBackgroundUpload / testBackgroundUploadHTTPS: GTMSessionUploadFetcher with a background
///   session for the chunks, as FirebaseStorage's putFile uses on a device: the start request goes
///   through ArumaRedirect, the chunks to the X-Goog-Upload-URL the server returned.
/// - testBackgroundSessionSkipsURLProtocol: a URLProtocol that answers everything, in an ephemeral
///   and in a background configuration: which one reaches the server.
///
/// LoopbackGuard refuses every request that is neither to this machine nor taken by ArumaRedirect,
/// in the sessions it can see (default and ephemeral configurations).
final class StoragePathTests: XCTestCase {
  private static let env = ProcessInfo.processInfo.environment
  private var stubHTTP: String { Self.env["STUB_HTTP"] ?? "http://127.0.0.1:8824" }
  private var stubHTTPS: String { Self.env["STUB_HTTPS"] ?? "https://localhost:8825" }
  private var results: [[String: Any]] = []

  override class func setUp() {
    super.setUp()
    LoopbackGuard.install()
  }

  private func step(_ op: String, _ fn: () async throws -> String) async {
    do {
      results.append(["op": op, "ok": true, "detail": try await fn()])
    } catch {
      let ns = error as NSError
      var msg = "\(ns.domain) \(ns.code): \(ns.localizedDescription)"
      if let under = ns.userInfo[NSUnderlyingErrorKey] as? NSError {
        msg += " / \(under.domain) \(under.code): \(under.localizedDescription)"
      }
      results.append(["op": op, "ok": false, "detail": String(msg.prefix(240))])
    }
  }

  private func report(_ scenario: String) {
    let out: [String: Any] = [
      "scenario": scenario, "results": results,
      "blocked": LoopbackGuard.blocked,
      "redirected": LoopbackGuard.redirected.count,
    ]
    let data = try! JSONSerialization.data(withJSONObject: out)
    print("S0PATH_RESULT " + String(data: data, encoding: .utf8)!)
  }

  private static func redact(_ url: URL) -> String {
    url.absoluteString.replacingOccurrences(of: #"token=[^&]*"#, with: "token=<redacted>", options: .regularExpression)
  }

  func testEmulator() async throws { try await storagePath("emulator") }
  func testProdAsIs() async throws { try await storagePath("prod-asis") }
  func testProdSpike() async throws { try await storagePath("prod-spike") }
  func testProdSpikeHTTPS() async throws { try await storagePath("prod-spike-https") }

  private func storagePath(_ mode: String) async throws {
    if mode != "emulator" {
      try ArumaRedirect.install(urlString: mode.hasSuffix("https") ? stubHTTPS : stubHTTP)
      ArumaRedirect.setStorageSpike(mode.hasPrefix("prod-spike"))
    }
    let options = FirebaseOptions(googleAppID: "1:123456789012:ios:0123456789abcdef", gcmSenderID: "1")
    options.apiKey = "AIzaS0pathNotARealKey"
    options.projectID = "s0path"
    options.storageBucket = "s0path.appspot.com"
    options.bundleID = Bundle.main.bundleIdentifier ?? "com.aruma.s0path"
    FirebaseApp.configure(name: "s0path", options: options)
    let storage = Storage.storage(app: FirebaseApp.app(name: "s0path")!)
    if mode == "emulator" {
      storage.useEmulator(withHost: "127.0.0.1", port: URL(string: stubHTTP)!.port!)
    }
    // A blocked request is retried until these run out (defaults: 2 and 10 minutes).
    storage.maxOperationRetryTime = 4
    storage.maxUploadRetryTime = 4
    storage.maxDownloadRetryTime = 4
    results.append(["op": "init", "ok": true, "detail": "redirect=\(ArumaRedirect.baseURL?.absoluteString ?? "none")"])

    let small = Data("hello aruma\n".utf8)
    let big = Data(repeating: 7, count: 3 * 1024 * 1024 + 17)
    let bigFile = FileManager.default.temporaryDirectory.appendingPathComponent("big-upload.bin")
    try big.write(to: bigFile)
    let root = storage.reference()
    let smallRef = root.child("s0/small.txt"), bigRef = root.child("s0/big.bin"), fileRef = root.child("s0/file.bin")
    var url: URL?

    let textMeta = StorageMetadata()
    textMeta.contentType = "text/plain"
    await step("upload.small") { "\(try await smallRef.putDataAsync(small, metadata: textMeta).size)" }
    await step("upload.resumable3MB") { "\(try await bigRef.putDataAsync(big).size)" }
    await step("upload.putFile3MB") { "\(try await fileRef.putFileAsync(from: bigFile).size)" }
    await step("getMetadata") { "size=\(try await smallRef.getMetadata().size)" }
    await step("getDownloadURL") {
      url = try await smallRef.downloadURL()
      return Self.redact(url!)
    }
    await step("download.data") {
      let got = try await bigRef.data(maxSize: 16 * 1024 * 1024)
      guard got == big else { throw URLError(.cannotDecodeContentData) }
      return "\(got.count) bytes"
    }
    await step("download.writeToFile") {
      let dest = FileManager.default.temporaryDirectory.appendingPathComponent("big-download.bin")
      try? FileManager.default.removeItem(at: dest)
      let written = try await fileRef.writeAsync(toFile: dest)
      return "\(try Data(contentsOf: written).count) bytes"
    }
    await step("list") { "\(try await root.child("s0").listAll().items.count) items" }
    await step("downloadURL.URLSession.shared") {
      guard let url else { throw URLError(.badURL) }
      let (data, response) = try await URLSession.shared.data(from: url)
      return "\((response as! HTTPURLResponse).statusCode) \(data.count) bytes from \(url.host!)"
    }
    await step("delete") {
      try await smallRef.delete()
      try await bigRef.delete()
      try await fileRef.delete()
      return ""
    }
    report("ios-\(mode)")
  }

  func testBackgroundUpload() async throws { try await backgroundUpload(https: false) }
  func testBackgroundUploadHTTPS() async throws { try await backgroundUpload(https: true) }

  /// GTMSessionUploadFetcher as StorageUploadTask builds it for putFile, with the background
  /// session FirebaseStorage turns on outside the simulator (StorageUploadTask.swift, `#if
  /// targetEnvironment(simulator)`).
  private func backgroundUpload(https: Bool) async throws {
    try ArumaRedirect.install(urlString: https ? stubHTTPS : stubHTTP)
    ArumaRedirect.setStorageSpike(true)
    let file = FileManager.default.temporaryDirectory.appendingPathComponent("bg-upload.bin")
    try Data(repeating: 9, count: 1024 * 1024 + 3).write(to: file)
    var request = URLRequest(url: URL(string: "https://firebasestorage.googleapis.com/v0/b/s0path.appspot.com/o?uploadType=resumable&name=s0%2Fbg.bin")!)
    request.httpMethod = "POST"
    request.setValue("application/json; charset=UTF-8", forHTTPHeaderField: "Content-Type")
    request.httpBody = Data(#"{"name":"s0/bg.bin","contentType":"application/octet-stream"}"#.utf8)
    let fetcher = GTMSessionUploadFetcher(request: request, uploadMIMEType: "application/octet-stream",
                                          chunkSize: Int64.max, fetcherService: nil)
    fetcher.uploadFileURL = file
    fetcher.useBackgroundSession = true
    fetcher.allowLocalhostRequest = true
    fetcher.allowedInsecureSchemes = ["http"]
    var chunkInBackground: [Bool] = []
    var chunkHosts: Set<String> = []
    fetcher.sendProgressBlock = { _, _, _ in
      if let chunk = fetcher.chunkFetcher {
        chunkInBackground.append(chunk.isUsingBackgroundSession)
        if let url = chunk.request?.url { chunkHosts.insert("\(url.scheme ?? "?")://\(url.host ?? "?"):\(url.port ?? 0)") }
      }
    }
    await step("background.putFile1MB") {
      let data = try await withCheckedThrowingContinuation { (c: CheckedContinuation<Data?, Error>) in
        fetcher.beginFetch { data, error in
          if let error { c.resume(throwing: error) } else { c.resume(returning: data) }
        }
      }
      let size = (try JSONSerialization.jsonObject(with: data ?? Data()) as? [String: Any])?["size"] ?? "?"
      return "size=\(size), chunks sent to \(chunkHosts.sorted()), chunk fetcher in a background session: \(Set(chunkInBackground))"
    }
    report(https ? "ios-background-upload-https" : "ios-background-upload")
  }

  /// A URLProtocol that answers every request itself (status 299), so a request that reaches the
  /// server did not go through it.
  final class ProbeProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canInit(with task: URLSessionTask) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
      let response = HTTPURLResponse(url: request.url!, statusCode: 299, httpVersion: "HTTP/1.1", headerFields: nil)!
      client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
      client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
  }

  final class Delegate: NSObject, URLSessionDataDelegate {
    var done: ((Int, Error?) -> Void)?
    var status = 0
    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
      status = (response as? HTTPURLResponse)?.statusCode ?? 0
      completionHandler(.allow)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
      if status == 0 { status = (task.response as? HTTPURLResponse)?.statusCode ?? 0 }
      done?(status, error)
    }
  }

  func testBackgroundSessionSkipsURLProtocol() async throws {
    let file = FileManager.default.temporaryDirectory.appendingPathComponent("probe.bin")
    try Data("probe".utf8).write(to: file)
    for kind in ["ephemeral", "background"] {
      let config = kind == "ephemeral" ? URLSessionConfiguration.ephemeral
        : URLSessionConfiguration.background(withIdentifier: "com.aruma.s0path.probe.\(UUID().uuidString)")
      config.protocolClasses = [ProbeProtocol.self] + (config.protocolClasses ?? [])
      let delegate = Delegate()
      let session = URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
      var request = URLRequest(url: URL(string: "\(stubHTTP)/healthz?probe=\(kind)")!)
      request.httpMethod = "PUT"
      await step("probe.\(kind)") {
        let status = try await withCheckedThrowingContinuation { (c: CheckedContinuation<Int, Error>) in
          delegate.done = { status, error in
            if let error { c.resume(throwing: error) } else { c.resume(returning: status) }
          }
          session.uploadTask(with: request, fromFile: file).resume()
        }
        return status == 299 ? "answered by the URLProtocol (299)" : "reached the server (\(status))"
      }
      session.finishTasksAndInvalidate()
    }
    report("ios-background-probe")
  }
}
