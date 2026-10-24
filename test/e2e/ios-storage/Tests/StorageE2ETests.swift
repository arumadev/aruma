import ArumaRedirect
import FirebaseAuth
import FirebaseCore
import FirebaseStorage
#if canImport(GTMSessionFetcherCore)
  import GTMSessionFetcherCore  // SwiftPM
#else
  import GTMSessionFetcher  // CocoaPods (cocoapods/)
#endif
import XCTest

/// The production path of Cloud Storage on iOS (ADR-022 §16.3): the official app code, the official
/// firebase-ios-sdk URL and version, resolved through `swift package config set-mirror` to the
/// patched FirebaseStorage (or, in cocoapods/, the FirebaseStorage pod patched by
/// aruma_storage.rb), which reads ArumaURL from the host app's Info.plist. No `useEmulator`,
/// and ArumaRedirect only carries Auth: ArumaStorageRedirect is false, so a Storage request to
/// Google would fail. Signed in anonymously, under the rules of storage.rules.
///
/// run.sh passes the server through TEST_RUNNER_ARUMA_URL (the address the server listens on),
/// TEST_RUNNER_ARUMA_STORAGE_URL (the ArumaURL written into Info.plist, its host in upper case) and
/// TEST_RUNNER_ARUMA_PROJECT_ID. URLSession requests other than to this machine are refused
/// (LoopbackGuard), and run.sh checks the process's sockets.
final class StorageE2ETests: XCTestCase {
  private static let env = ProcessInfo.processInfo.environment
  private var serverURL: String { Self.env["ARUMA_URL"] ?? "" }
  /// The unified form's origin: ArumaURL with its host in lower case.
  private var unifiedOrigin: String {
    let raw = Self.env["ARUMA_STORAGE_URL"] ?? ""
    var c = URLComponents(string: raw)!
    c.host = c.host?.lowercased()
    return c.string!
  }

  /// Tells this run's objects apart from an earlier run's on the same server.
  private static let run = String(UInt64(Date().timeIntervalSince1970 * 1000), radix: 36)

  private var storage: Storage { Storage.storage() }
  private var bucket: String { storage.reference().bucket }
  private var blockedBefore = 0
  private var redirectedBefore = 0

  override class func setUp() {
    super.setUp()
    LoopbackGuard.install()
    FetcherLog.install()
    BackgroundUploads.install()
    if FirebaseApp.app() == nil {
      FirebaseApp.configure()  // GoogleService-Info.plist from the host app's bundle
    }
  }

  override class func tearDown() {
    let refused = LoopbackGuard.blocked
    print("ARUMA_E2E guard: \(LoopbackGuard.loopbackPaths.count) requests to Aruma, "
      + "\(LoopbackGuard.redirected.count) requests to Google hosts taken by ArumaRedirect "
      + "\(LoopbackGuard.redirected), \(refused.count) refused \(refused)")
    super.tearDown()
  }

  override func setUp() async throws {
    struct NotConfigured: Error, CustomStringConvertible {
      var description: String { "run through test/e2e/ios-storage/run.sh (ARUMA_URL is not set)" }
    }
    guard !serverURL.isEmpty else { throw NotConfigured() }
    blockedBefore = LoopbackGuard.blocked.count
    redirectedBefore = LoopbackGuard.redirected.count
    if Auth.auth().currentUser == nil {
      _ = try await Auth.auth().signInAnonymously()
    }
    // A failed request is retried until these run out (defaults: 2 and 10 minutes).
    storage.maxOperationRetryTime = 10
    storage.maxUploadRetryTime = 20
    storage.maxDownloadRetryTime = 20
  }

  override func tearDown() {
    // The guard: nothing but this machine was asked for, and no Storage request went to Google
    // (the run-time fallback would have refused it; only Auth may be redirected).
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)), [], "requests leaving the machine")
    let storageRedirected = LoopbackGuard.redirected.dropFirst(redirectedBefore)
      .filter { $0.hasPrefix("firebasestorage.googleapis.com") }
    XCTAssertEqual(Array(storageRedirected), [], "Storage requests to Google")
    super.tearDown()
  }

  private func folder() throws -> StorageReference {
    let uid = try XCTUnwrap(Auth.auth().currentUser?.uid)
    return storage.reference().child("e2e/\(uid)/\(Self.run)")
  }

  // MARK: 0. ArumaRedirect found the patched build at launch.

  func testFirebaseStorageIsTheArumaBuild() {
    XCTAssertEqual(ArumaRedirect.firebaseStorageBuild, .patched)
    XCTAssertNotNil(NSClassFromString("FIRArumaStorageBuild"))
  }

  // MARK: 1. The eleven operations, and the download URL in the unified form.

  func testElevenOperations() async throws {
    var results: [(op: String, ok: Bool, detail: String)] = []
    func step(_ op: String, _ fn: () async throws -> String) async {
      do {
        results.append((op, true, try await fn()))
      } catch {
        let ns = error as NSError
        results.append((op, false, "\(ns.domain) \(ns.code): \(ns.localizedDescription)"))
      }
    }

    let dir = try folder()
    let small = Data("hello aruma\n".utf8)
    let big = Data((0 ..< 3 * 1024 * 1024 + 17).map { UInt8(truncatingIfNeeded: $0 &* 31 &+ 7) })
    let bigFile = FileManager.default.temporaryDirectory.appendingPathComponent("big-upload.bin")
    try big.write(to: bigFile)
    let smallRef = dir.child("small.txt"), bigRef = dir.child("big.bin"), fileRef = dir.child("file.bin")
    var link: URL?

    await step("putData") {
      let meta = StorageMetadata()
      meta.contentType = "text/plain"
      meta.customMetadata = ["origin": "ios-e2e"]
      return "\(try await smallRef.putDataAsync(small, metadata: meta).size) bytes"
    }
    await step("putData 3 MB (resumable)") { "\(try await bigRef.putDataAsync(big).size) bytes" }
    await step("putFile 3 MB") { "\(try await fileRef.putFileAsync(from: bigFile).size) bytes" }
    await step("getMetadata") {
      let m = try await smallRef.getMetadata()
      guard m.size == Int64(small.count), m.contentType == "text/plain",
            m.customMetadata == ["origin": "ios-e2e"] else { throw Failure("metadata \(m)") }
      return "size=\(m.size) contentType=\(m.contentType ?? "")"
    }
    await step("updateMetadata") { try await self.updateMetadata(smallRef) }
    await step("downloadURL (unified form)") {
      let url = try await smallRef.downloadURL()
      link = url
      let token = try await self.serverToken(of: smallRef)
      let expected = "\(self.unifiedOrigin)/v0/b/\(self.bucket)/o/"
        + smallRef.fullPath.replacingOccurrences(of: "/", with: "%2F") + "?alt=media&token=\(token)"
      guard url.absoluteString == expected else {
        throw Failure("downloadURL \(Self.redact(url)) is not the unified form \(Self.redact(URL(string: expected)!))")
      }
      guard !url.absoluteString.contains(":443"), url.host == url.host?.lowercased() else {
        throw Failure("downloadURL \(Self.redact(url))")
      }
      return Self.redact(url)
    }
    await step("data 3 MB") {
      let got = try await bigRef.data(maxSize: 16 * 1024 * 1024)
      guard got == big else { throw Failure("\(got.count) bytes, not the uploaded ones") }
      return "\(got.count) bytes, equal"
    }
    await step("write(toFile:)") {
      let dest = FileManager.default.temporaryDirectory.appendingPathComponent("big-download.bin")
      try? FileManager.default.removeItem(at: dest)
      let written = try await fileRef.writeAsync(toFile: dest)
      guard try Data(contentsOf: written) == big else { throw Failure("the file differs") }
      return "\(big.count) bytes, equal"
    }
    await step("listAll") {
      let names = try await dir.listAll().items.map(\.name).sorted()
      guard names == ["big.bin", "file.bin", "small.txt"] else { throw Failure("\(names)") }
      return "\(names)"
    }
    await step("downloadURL with URLSession.shared") {
      guard let link else { throw Failure("no download URL") }
      let (data, response) = try await URLSession.shared.data(from: link)
      let status = (response as! HTTPURLResponse).statusCode
      guard status == 200, data == small else { throw Failure("\(status), \(data.count) bytes") }
      return "\(status), \(data.count) bytes from \(link.host!):\(link.port.map(String.init) ?? "-")"
    }
    await step("delete") {
      for ref in [smallRef, bigRef, fileRef] {
        try await ref.delete()
      }
      do {
        _ = try await smallRef.getMetadata()
        throw Failure("small.txt is still there")
      } catch let error as NSError where error.domain == StorageErrorDomain
        && error.code == StorageErrorCode.objectNotFound.rawValue {
        return "3 deleted, getMetadata: objectNotFound"
      }
    }

    for r in results {
      print("ARUMA_E2E op \(r.ok ? "ok  " : "FAIL") \(r.op): \(r.detail)")
    }
    let passed = results.filter(\.ok).count
    print("ARUMA_E2E operations: \(passed)/\(results.count)")
    XCTAssertEqual(results.count, 11)
    XCTAssertEqual(passed, results.count, results.filter { !$0.ok }.map { "\($0.op): \($0.detail)" }.joined(separator: "; "))
  }

  /// The real PATCH bodies FirebaseStorage sends (ADR-023 §3-17, A §8.1), recorded from the
  /// fetchers themselves, and what the server made of them.
  private func updateMetadata(_ ref: StorageReference) async throws -> String {
    // 1. A new StorageMetadata, as an app writes `updateMetadata(StorageMetadata())` with one field.
    let fresh = StorageMetadata()
    fresh.cacheControl = "no-cache"
    let mark1 = FetcherLog.count
    let after1 = try await ref.updateMetadata(fresh)
    let body1 = try FetcherLog.lastBody(method: "PATCH", since: mark1)

    // 2. The metadata read from the server, with one field changed. It sends null for the fields
    // StorageMetadata does not model, downloadTokens among them: the token must survive.
    let tokenBefore = try await serverToken(of: ref)
    let read = try await ref.getMetadata()
    read.customMetadata = ["origin": "ios-e2e", "step": "2"]
    let mark2 = FetcherLog.count
    let after2 = try await ref.updateMetadata(read)
    let body2 = try FetcherLog.lastBody(method: "PATCH", since: mark2)
    let tokenKept = try await serverToken(of: ref) == tokenBefore

    for (name, body, after) in [("new StorageMetadata()", body1, after1), ("getMetadata(), customMetadata changed", body2, after2)] {
      var record: [String: Any] = [
        "case": name, "method": "PATCH", "body": String(decoding: body, as: UTF8.self),
        "customMetadataAfter": after.customMetadata.map { $0 as Any } ?? NSNull(),
        "cacheControlAfter": after.cacheControl.map { $0 as Any } ?? NSNull(),
      ]
      if after === after2 {
        record["downloadTokenKept"] = tokenKept
      }
      let json = try JSONSerialization.data(withJSONObject: record, options: [.sortedKeys])
      print("ARUMA_E2E_UPDATE_METADATA " + String(decoding: json, as: UTF8.self))
    }
    guard after1.cacheControl == "no-cache", after2.customMetadata?["step"] == "2" else {
      throw Failure("after: \(after1), \(after2)")
    }
    // "downloadTokens": null must not revoke the token (A §8.1): links already shared keep working.
    guard tokenKept else { throw Failure("updateMetadata with downloadTokens: null changed the download token") }
    let sentNull = (try JSONSerialization.jsonObject(with: body1) as? [String: Any])?["metadata"] is NSNull
    return "new StorageMetadata() sends \"metadata\": \(sentNull ? "null" : "no null"); custom metadata after it: "
      + "\(after1.customMetadata.map { "\($0)" } ?? "none")"
  }

  // MARK: 2. Names with spaces, '+', non-ASCII and Arabic: three references, one object.

  func testNamesRoundTripThroughTheirLinks() async throws {
    let dir = try folder().child("names")
    for name in ["a b+é.txt", "صورة ١.png"] {
      let original = dir.child(name)
      let bytes = Data("bytes of \(name)\n".utf8)
      _ = try await original.putDataAsync(bytes)
      let link = try await original.downloadURL().absoluteString
      XCTAssertTrue(link.hasPrefix("\(unifiedOrigin)/v0/b/\(bucket)/o/"), link)

      let gs = try ArumaUrls.fromArumaUrl(link, bucket: bucket)
      XCTAssertEqual(gs, "gs://\(bucket)/\(original.fullPath)")
      let fromLink = storage.reference(forURL: link)
      let fromGS = storage.reference(forURL: gs)
      for ref in [fromLink, fromGS] {
        XCTAssertEqual(ref.fullPath, original.fullPath, name)
        XCTAssertEqual(ref.bucket, original.bucket, name)
        let got = try await ref.data(maxSize: 1 << 20)
        XCTAssertEqual(got, bytes, name)
      }
      print("ARUMA_E2E name \(name): \(link.components(separatedBy: "?")[0]) -> \(gs)")
      try await original.delete()
    }
  }

  // MARK: 3. putFile with a background session, as on a device.

  /// FirebaseStorage turns the background session off on the simulator (StorageUploadTask.swift,
  /// `#if targetEnvironment(simulator)`); BackgroundUploads keeps it on, so putFile runs as on a
  /// device: `start` from the ordinary session, the chunks from a background URLSession, which no
  /// URLProtocol sees, to the upload URL the server returned.
  func testPutFileWithBackgroundSessionGoesToAruma() async throws {
    let file = FileManager.default.temporaryDirectory.appendingPathComponent("bg-upload.bin")
    let payload = Data((0 ..< 1024 * 1024 + 3).map { UInt8(truncatingIfNeeded: $0 &* 13) })
    try payload.write(to: file)
    let ref = try folder().child("background.bin")
    let mark = FetcherLog.count
    BackgroundUploads.on = true
    defer { BackgroundUploads.on = false }
    let meta = try await ref.putFileAsync(from: file)
    XCTAssertEqual(meta.size, Int64(payload.count))

    let started = FetcherLog.entries(since: mark)
    let background = started.filter(\.background)
    XCTAssertFalse(background.isEmpty, "no fetcher used a background session: \(started)")
    let server = URL(string: serverURL)!
    for f in background {
      XCTAssertEqual(f.host, server.host, "\(f)")
      XCTAssertEqual(f.port, server.port, "\(f)")
      XCTAssertTrue(f.query.contains("upload_id="), "\(f)")
    }
    let foreground = started.filter { !$0.background }
    XCTAssertTrue(foreground.allSatisfy { $0.host == URL(string: unifiedOrigin)!.host || $0.host == server.host },
                  "\(foreground)")
    print("ARUMA_E2E background putFile: \(background.count) chunk request(s) from a background session to "
      + "\(background.map { "\($0.method) \($0.host):\($0.port ?? 0)\($0.path)" }), start: "
      + "\(foreground.map { "\($0.method) \($0.host):\($0.port ?? 0)\($0.path)" })")
    let got = try await ref.data(maxSize: 4 << 20)
    XCTAssertEqual(got, payload)
    try await ref.delete()
  }

  // MARK: helpers

  /// The object's first download token, as the server stores it (the /v0 metadata the SDKs read).
  private func serverToken(of ref: StorageReference) async throws -> String {
    let token = try await XCTUnwrap(Auth.auth().currentUser).getIDToken()
    let name = ref.fullPath.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
    var request = URLRequest(url: URL(string: "\(serverURL)/v0/b/\(bucket)/o/\(name)")!)
    request.setValue("Firebase \(token)", forHTTPHeaderField: "Authorization")
    let (data, response) = try await URLSession.shared.data(for: request)
    guard (response as? HTTPURLResponse)?.statusCode == 200,
          let tokens = (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["downloadTokens"] as? String,
          let first = tokens.split(separator: ",").first, UUID(uuidString: String(first)) != nil
    else { throw Failure("no download token: \(String(decoding: data, as: UTF8.self))") }
    return String(first)
  }

  private static func redact(_ url: URL) -> String {
    url.absoluteString.replacingOccurrences(of: #"token=[^&]*"#, with: "token=<uuid>", options: .regularExpression)
  }
}

struct Failure: Error, CustomStringConvertible {
  let description: String
  init(_ description: String) { self.description = description }
}

/// Every GTMSessionFetcher that starts (FirebaseStorage's requests, and the chunk fetchers of its
/// uploads): method, address, whether it runs in a background session, and the body it carries.
enum FetcherLog {
  struct Entry: CustomStringConvertible {
    let method: String, host: String, port: Int?, path: String, query: String
    let background: Bool
    let body: Data?
    var description: String { "\(method) \(host):\(port ?? 0)\(path) background=\(background)" }
  }

  private static let lock = NSLock()
  private static var log: [Entry] = []
  private static var observer: NSObjectProtocol?

  static var count: Int { lock.lock(); defer { lock.unlock() }; return log.count }

  static func entries(since mark: Int) -> [Entry] {
    lock.lock(); defer { lock.unlock() }
    return Array(log.dropFirst(mark))
  }

  static func lastBody(method: String, since mark: Int) throws -> Data {
    guard let body = entries(since: mark).last(where: { $0.method == method })?.body else {
      throw Failure("no \(method) request with a body was started")
    }
    return body
  }

  static func install() {
    guard observer == nil else { return }
    observer = NotificationCenter.default.addObserver(
      forName: NSNotification.Name("kGTMSessionFetcherStartedNotification"), object: nil, queue: nil
    ) { note in
      guard let fetcher = note.object as? GTMSessionFetcher, let url = fetcher.request?.url else { return }
      let c = URLComponents(url: url, resolvingAgainstBaseURL: false)
      let entry = Entry(method: fetcher.request?.httpMethod ?? "GET", host: url.host ?? "", port: url.port,
                        path: url.path, query: c?.percentEncodedQuery ?? "",
                        background: fetcher.isUsingBackgroundSession,
                        body: fetcher.request?.httpBody ?? fetcher.bodyData)
      lock.lock(); log.append(entry); lock.unlock()
    }
  }
}

/// Keeps `useBackgroundSession` on for GTMSessionUploadFetcher while `on` is set, whatever
/// FirebaseStorage asks for.
enum BackgroundUploads {
  private static let lock = NSLock()
  private static var _on = false
  static var on: Bool {
    get { lock.lock(); defer { lock.unlock() }; return _on }
    set { lock.lock(); _on = newValue; lock.unlock() }
  }

  private static var installed = false

  static func install() {
    guard !installed else { return }
    installed = true
    let selector = NSSelectorFromString("setUseBackgroundSession:")
    let method = class_getInstanceMethod(GTMSessionUploadFetcher.self, selector)!
    typealias Setter = @convention(c) (AnyObject, Selector, ObjCBool) -> Void
    let original = unsafeBitCast(method_getImplementation(method), to: Setter.self)
    let replacement: @convention(block) (AnyObject, ObjCBool) -> Void = { fetcher, value in
      original(fetcher, selector, on ? true : value)
    }
    method_setImplementation(method, imp_implementationWithBlock(replacement))
  }
}
