import ArumaRedirect
import FirebaseAuth
import FirebaseCore
import FirebaseDatabase
import XCTest

/// The official FirebaseDatabase for iOS in production mode, configured only with the
/// GoogleService-Info.plist Aruma generated (its DATABASE_URL is the database), signed in with
/// FirebaseAuth through ArumaRedirect, against a real server under the rules of
/// test/e2e/rtdb-rules.json. Two SDK clients run in the process (the default app and a second
/// one), and the database's REST API with an admin credential is the third party. URLSession
/// requests other than to this machine are refused (LoopbackGuard); the database's own WebSockets
/// do not go through URLSession, and run.sh checks the process's sockets for them. The scenarios
/// follow test/e2e/web-rtdb.
///
/// run.sh passes the server through TEST_RUNNER_ARUMA_URL, TEST_RUNNER_ARUMA_PROJECT_ID,
/// TEST_RUNNER_ARUMA_CLI_TOKEN and TEST_RUNNER_ARUMA_SWITCH_URL (the server's off switch).
final class RtdbE2ETests: XCTestCase {
  private static let env = ProcessInfo.processInfo.environment
  private var arumaURL: String { Self.env["ARUMA_URL"] ?? "" }
  private var projectID: String { Self.env["ARUMA_PROJECT_ID"] ?? "" }
  private var ns: String { "\(projectID)-default-rtdb" }
  /// Tells this run's paths and addresses apart from an earlier run's on the same server.
  private static let run = String(UInt64(Date().timeIntervalSince1970 * 1000), radix: 36)

  private var auth: Auth { Auth.auth() }
  private var db: Database { Database.database() }
  /// The second client: another FirebaseApp with the same options, its own user and connection.
  private static var second: FirebaseApp!
  private var auth2: Auth { Auth.auth(app: Self.second) }
  private var db2: Database { Database.database(app: Self.second) }

  private var blockedBefore = 0
  private var observers: [(DatabaseQuery, DatabaseHandle)] = []

  override class func setUp() {
    super.setUp()
    LoopbackGuard.install()
    if FirebaseApp.app() == nil {
      FirebaseApp.configure()  // GoogleService-Info.plist from the host app's bundle
    }
    if second == nil, let options = FirebaseApp.app()?.options {
      FirebaseApp.configure(name: "second", options: options)
      second = FirebaseApp.app(name: "second")
    }
  }

  override class func tearDown() {
    let refused = LoopbackGuard.blocked
    print("ARUMA_E2E guard: \(LoopbackGuard.loopbackPaths.count) requests to Aruma, "
      + "\(LoopbackGuard.redirected.count) Auth requests to Google hosts redirected by ArumaRedirect, "
      + "\(refused.count) refused \(refused)")
    super.tearDown()
  }

  override func setUpWithError() throws {
    struct NotConfigured: Error, CustomStringConvertible {
      var description: String { "run through test/e2e/ios-rtdb/run.sh (ARUMA_URL is not set)" }
    }
    guard !arumaURL.isEmpty else { throw NotConfigured() }
    blockedBefore = LoopbackGuard.blocked.count
    try auth.signOut()
    try auth2.signOut()
    db.goOnline()
    db2.goOnline()
  }

  override func tearDown() {
    for (q, h) in observers {
      q.removeObserver(withHandle: h)
    }
    observers = []
    // The guard: nothing but this machine was asked for during the test.
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)), [], "requests leaving the machine")
    super.tearDown()
  }

  // MARK: 1. Sign-in, and the database SDK in production mode on the generated URL.

  func testProductionModeOnTheGeneratedDatabaseURL() async throws {
    XCTAssertTrue(ArumaRedirect.loaderRan)
    XCTAssertEqual(ArumaRedirect.baseURL?.absoluteString, arumaURL)
    let options = try XCTUnwrap(FirebaseApp.app()?.options)
    XCTAssertEqual(options.databaseURL, "\(arumaURL)/?ns=\(ns)")
    // The SDK took host and namespace from that URL: the root reference is on Aruma.
    XCTAssertEqual(db.reference().url, arumaURL)
    for name in ["FIRInstallations", "FIRAnalytics", "FIRMessaging"] {
      XCTAssertNil(NSClassFromString(name), "\(name) is linked in")
    }
    let uid = try await auth.createUser(withEmail: "signin-ios-\(Self.run)@example.com", password: "secret1").user.uid
    XCTAssertEqual(uid.count, 28)
    // A write under the user's own path: the connection carries Aruma's ID token.
    try await db.reference(withPath: "users/\(uid)/profile").setValue(["name": "سارة"])
    let stored = try await rest("GET", "users/\(uid)/profile")
    XCTAssertEqual(stored.json as? [String: String], ["name": "سارة"])
  }

  // MARK: 2. Rules: own data yes, another user's no.

  func testRulesAllowOwnDataAndDenyOthers() async throws {
    let uid = try await auth.signInAnonymously().user.uid
    let mine = db.reference(withPath: "users/\(uid)")
    try await mine.setValue(["note": "mine"])
    let read = try await mine.getData()
    XCTAssertEqual(read.value as? [String: String], ["note": "mine"])

    let other = "other-\(Self.run)"
    let put = try await rest("PUT", "users/\(other)", #"{"note":"theirs"}"#)
    XCTAssertEqual(put.status, 200)
    let theirs = db.reference(withPath: "users/\(other)")
    let denied = await writeError(theirs.child("note"), "overwritten")
    XCTAssertEqual(denied?.code, 1, "permission_denied")
    let r = listen(theirs)
    try await eventually("the listen is cancelled") { r.events.last?.kind == "cancelled" }
    XCTAssertEqual(r.events.last?.error?.code, 1)
    do {
      _ = try await theirs.getData()
      XCTFail("getData() of another user's data succeeded")
    } catch {
      XCTAssertTrue("\(error)".localizedCaseInsensitiveContains("permission"), "\(error)")
    }
    let stored = try await rest("GET", "users/\(other)")
    XCTAssertEqual(stored.json as? [String: String], ["note": "theirs"])

    // Signed out, not even the own data.
    try auth.signOut()
    try await eventually("own data denied when signed out") { await self.writeError(mine.child("note"), "again") != nil }
  }

  // MARK: 3. Two clients and the admin: writes reach the others' listeners, in order.

  func testWritesReachTheOtherClientsInOrderWithServerTimestamps() async throws {
    let ua = try await auth.createUser(withEmail: "chat-ios-\(Self.run)@example.com", password: "secret1").user.uid
    let ub = try await auth2.signInAnonymously().user.uid
    let path = "rooms/ios-\(Self.run)/messages"
    let addedA = listen(db.reference(withPath: path).queryOrdered(byChild: "ts"), .childAdded)
    let addedB = listen(db2.reference(withPath: path).queryOrdered(byChild: "ts"), .childAdded)
    let allB = listen(db2.reference(withPath: path).queryOrdered(byChild: "ts"))

    let t0 = Date().timeIntervalSince1970 * 1000
    var keys: [String] = []
    // Each write reaches both listeners before the next one is made.
    func arrived(_ key: String) async throws {
      try await eventually("\(key) at both clients") {
        addedA.events.contains { $0.key == key } && addedB.events.contains { $0.key == key }
      }
    }
    for (database, from, text) in [(db, ua, "a1"), (db2, ub, "b1"), (db, ua, "a2")] {
      let m = database.reference(withPath: path).childByAutoId()
      try await m.setValue(["from": from, "text": text, "ts": ServerValue.timestamp()])
      keys.append(try XCTUnwrap(m.key))
      try await arrived(keys.last!)
    }
    let posted = try await rest("POST", path, #"{"from":"admin","text":"admin","ts":{".sv":"timestamp"}}"#)
    XCTAssertEqual(posted.status, 200)
    keys.append(try XCTUnwrap((posted.json as? [String: String])?["name"]))
    try await arrived(keys.last!)
    let t1 = Date().timeIntervalSince1970 * 1000

    XCTAssertEqual(addedA.events.compactMap(\.key), keys)
    XCTAssertEqual(addedB.events.compactMap(\.key), keys)
    // The final value: children in the order of ts, each ts the server's time as REST reads it.
    let room = try await rest("GET", path)
    let stored = try XCTUnwrap(room.json as? [String: [String: Any]])
    try await eventually("the server's timestamps at B") {
      guard let last = allB.events.last, last.keys == keys, let value = last.value as? [String: [String: Any]] else { return false }
      return keys.allSatisfy { (value[$0]?["ts"] as? NSNumber)?.int64Value == (stored[$0]?["ts"] as? NSNumber)?.int64Value }
    }
    var prev: Double = 0
    for k in keys {
      let ts = try XCTUnwrap((stored[k]?["ts"] as? NSNumber)?.doubleValue)
      XCTAssertTrue(ts >= t0 - 5000 && ts <= t1 + 5000, "ts \(ts) in [\(t0), \(t1)]")
      XCTAssertGreaterThanOrEqual(ts, prev)
      prev = ts
    }
    XCTAssertEqual(keys.map { stored[$0]?["text"] as? String }, ["a1", "b1", "a2", "admin"])
  }

  // MARK: 4. Queries: orderByChild with .indexOn, limitToLast, a child entering the window.

  func testQueryWindowFollowsWrites() async throws {
    _ = try await auth.signInAnonymously()
    let board = "boards/ios-\(Self.run)"
    let scores = [50, 10, 80, 30, 70, 20, 60, 40]
    let seed = scores.enumerated().map { #""p\#($0.offset)":{"score":\#($0.element)}"# }.joined(separator: ",")
    let put = try await rest("PUT", board, "{\(seed)}")
    XCTAssertEqual(put.status, 200)

    let top = db.reference(withPath: board).queryOrdered(byChild: "score").queryLimited(toLast: 3)
    let value = listen(top)
    let added = listen(top, .childAdded)
    let removed = listen(top, .childRemoved)
    try await eventually("the top three") { value.events.last?.keys == ["p6", "p4", "p2"] }
    XCTAssertEqual(added.events.map { "\($0.key ?? "") \($0.prev ?? "nil")" }, ["p6 nil", "p4 p6", "p2 p4"])

    // This client adds a child above the window's lowest: it enters after p4, p6 leaves.
    try await db.reference(withPath: "\(board)/p8").setValue(["score": 75])
    try await eventually("p8 in") { value.events.last?.keys == ["p4", "p8", "p2"] }
    XCTAssertEqual(added.events.last.map { "\($0.key ?? "") \($0.prev ?? "nil")" }, "p8 p4")
    XCTAssertEqual(removed.events.compactMap(\.key), ["p6"])

    // The admin writes below the window (nothing changes), then moves p1 to the top.
    let before = value.events.count
    let low = try await rest("PUT", "\(board)/p9", #"{"score":5}"#)
    XCTAssertEqual(low.status, 200)
    let patch = try await rest("PATCH", board, #"{"p1/score":99}"#)
    XCTAssertEqual(patch.status, 200)
    try await eventually("p1 in") { value.events.last?.keys == ["p8", "p2", "p1"] }
    XCTAssertEqual(value.events.count, before + 1)
    XCTAssertEqual(removed.events.compactMap(\.key), ["p6", "p4"])

    // A one-off query the server answers from its index: the two lowest.
    let first = try await db.reference(withPath: board).queryOrdered(byChild: "score").queryLimited(toFirst: 2).getData()
    XCTAssertEqual((first.children.allObjects as? [DataSnapshot])?.map(\.key), ["p9", "p5"])
  }

  // MARK: 5. Transactions: concurrent increments from both clients and the admin add up exactly.

  func testConcurrentTransactionsAddUpExactly() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let path = "counters/ios-\(Self.run)"
    let zero = try await rest("PUT", path, "0")
    XCTAssertEqual(zero.status, 200)
    let n = 15
    let refs = [db.reference(withPath: path), db2.reference(withPath: path)]
    let results = try await withThrowingTaskGroup(of: String.self) { group in
      for i in 0..<n {
        for ref in refs {
          group.addTask { try await Self.increment(ref) ? "committed" : "aborted" }
        }
        group.addTask { "admin \(try await self.adminIncrement(path)) \(i)" }
      }
      var out: [String] = []
      for try await r in group {
        out.append(r)
      }
      return out
    }
    XCTAssertEqual(results.filter { $0 == "committed" }.count, 2 * n)
    XCTAssertEqual(results.filter { $0.hasPrefix("admin") }.count, n)
    let final = try await rest("GET", path)
    XCTAssertEqual(final.body, "\(3 * n)")
    let seen = try await db.reference(withPath: path).getData()
    XCTAssertEqual((seen.value as? NSNumber)?.intValue, 3 * n)
  }

  // MARK: 6. onDisconnect presence: the other client sees offline when one disconnects.

  func testPresenceGoesOfflineWhenTheClientDisconnects() async throws {
    _ = try await auth.signInAnonymously()
    let ub = try await auth2.signInAnonymously().user.uid
    let mine = db2.reference(withPath: "status/\(ub)")
    func goOnline() async throws {
      try await mine.onDisconnectSetValue(["state": "offline", "at": ServerValue.timestamp()])
      try await mine.setValue(["state": "online", "at": ServerValue.timestamp()])
    }
    let status = listen(db.reference(withPath: "status/\(ub)"))
    func state() -> String? { (status.events.last?.value as? [String: Any])?["state"] as? String }

    try await goOnline()
    try await eventually("online") { state() == "online" }
    let t0 = Date()
    db2.goOffline()
    try await eventually("offline after goOffline") { state() == "offline" }
    print("ARUMA_E2E presence: offline \(Int(Date().timeIntervalSince(t0) * 1000)) ms after goOffline")
    XCTAssertNotNil((status.events.last?.value as? [String: Any])?["at"] as? NSNumber)

    // Back online the client registers again; the admin sees it too.
    db2.goOnline()
    try await goOnline()
    try await eventually("online again") { state() == "online" }
    db2.goOffline()
    try await eventually("offline for the admin") { try await self.rest("GET", "status/\(ub)/state").body == #""offline""# }
  }

  // MARK: 7. Offline: with the server down, a write is applied locally and lands when it is back.

  func testOfflineWriteLandsWhenTheServerIsBack() async throws {
    let uid = try await auth.signInAnonymously().user.uid
    let connected = listen(db.reference(withPath: ".info/connected"))
    try await eventually("connected", timeout: 30) { connected.events.last?.value as? Bool == true }
    let notes = db.reference(withPath: "users/\(uid)/notes")
    let local = listen(notes, .childAdded)
    try await notes.child("n0").setValue("online")

    let done = Completion()
    try await server("down")
    do {
      try await eventually("disconnected", timeout: 30) { connected.events.last?.value as? Bool == false }
      notes.child("n1").setValue(["text": "written offline", "at": ServerValue.timestamp()]) { error, _ in done.finish(error) }
      try await eventually("the local event", timeout: 5) { local.events.map(\.key) == ["n0", "n1"] }
      try await Task.sleep(nanoseconds: 3_000_000_000)
      XCTAssertFalse(done.finished, "the write completed with the server down")
    } catch {
      try await server("up")
      throw error
    }
    try await server("up")
    // The SDK reconnects by itself (its backoff is at most 30 s) and sends the queued write.
    try await eventually("the write landed", timeout: 90) { done.finished }
    XCTAssertNil(done.error)
    let note = try await rest("GET", "users/\(uid)/notes/n1")
    let stored = try XCTUnwrap(note.json as? [String: Any])
    XCTAssertEqual(stored["text"] as? String, "written offline")
    XCTAssertNotNil(stored["at"] as? NSNumber)
  }

  /// With disk persistence, as many apps run: the SDK re-listens after a reconnect with the hash of
  /// its cache, and for a cache over 1 KB a compound hash of ranges (ch). Whatever the server makes
  /// of them, the client must end up with exactly the server's data.
  func testPersistentCacheCatchesUpAfterReconnect() async throws {
    if FirebaseApp.app(name: "persist") == nil, let options = FirebaseApp.app()?.options {
      FirebaseApp.configure(name: "persist", options: options)
      Database.database(app: FirebaseApp.app(name: "persist")!).isPersistenceEnabled = true
    }
    let app = try XCTUnwrap(FirebaseApp.app(name: "persist"))
    let cached = Database.database(app: app)
    _ = try await Auth.auth(app: app).signInAnonymously()

    let board = "boards/ios-persist-\(Self.run)"
    let seed = (0..<60).map { String(format: #""p%02d":{"score":%d,"name":"player number %d"}"#, $0, $0, $0) }.joined(separator: ",")
    let put = try await rest("PUT", board, "{\(seed)}")
    XCTAssertEqual(put.status, 200)
    let ref = cached.reference(withPath: board)
    ref.keepSynced(true)
    let all = listen(ref)
    let top = listen(ref.queryOrdered(byChild: "score").queryLimited(toLast: 5))
    try await eventually("the board") { all.events.last?.keys.count == 60 }

    cached.goOffline()
    // Changes the client does not see while it is away: some children change, some go, some
    // arrive, above the query's window.
    var patch: [String] = []
    for i in 0..<5 {
      patch.append(String(format: #""p%02d/score":%d"#, i, 100 + i))
      patch.append(String(format: #""p%02d":null"#, 10 + i))
      patch.append(String(format: #""n%02d":{"score":%d,"name":"new"}"#, i, 200 + i))
    }
    let patched = try await rest("PATCH", board, "{\(patch.joined(separator: ","))}")
    cached.goOnline()
    XCTAssertEqual(patched.status, 200)

    let response = try await rest("GET", board)
    let stored = try XCTUnwrap(response.json as? NSDictionary)
    try await eventually("the server's board", timeout: 15) { (all.events.last?.value as? NSDictionary) == stored }
    try await eventually("the server's top five", timeout: 15) { top.events.last?.keys == ["n00", "n01", "n02", "n03", "n04"] }
  }

  // MARK: 8. Revocation: an admin disables the user; its listens are cancelled within seconds.

  func testDisablingTheUserCancelsItsListens() async throws {
    let uid = try await auth.createUser(withEmail: "revoked-ios-\(Self.run)@example.com", password: "secret1").user.uid
    let mine = db.reference(withPath: "users/\(uid)")
    try await mine.setValue(["secret": 1])
    let own = listen(mine)
    let room = listen(db.reference(withPath: "rooms/ios-revoked-\(Self.run)/messages"), .childAdded)
    try await eventually("the data") { own.events.count == 1 }

    let t0 = Date()
    try await disableUser(uid)
    try await eventually("both listens cancelled", timeout: 5) {
      own.events.last?.kind == "cancelled" && room.events.last?.kind == "cancelled"
    }
    print("ARUMA_E2E revocation: listens cancelled \(Int(Date().timeIntervalSince(t0) * 1000)) ms after the user was disabled")
    XCTAssertEqual(own.events.last?.error?.code, 1)
    XCTAssertEqual(room.events.last?.error?.code, 1)
    // The database SDK asked Auth for a fresh token, which a disabled user does not get: Auth signs
    // the user out.
    try await eventually("signed out", timeout: 15) { self.auth.currentUser == nil }
  }

  // MARK: 9. The network guard.

  /// The guard refuses what ArumaRedirect does not take, Firebase's database hosts included.
  func testGuardRefusesFirebaseDatabaseHosts() async throws {
    for raw in ["https://smoke-app-default-rtdb.firebaseio.com/.json",
                "https://smoke-app-default-rtdb.europe-west1.firebasedatabase.app/.json"] {
      let url = try XCTUnwrap(URL(string: raw))
      do {
        _ = try await URLSession(configuration: .ephemeral).data(from: url)
        XCTFail("the request was not refused")
      } catch {
        XCTAssertEqual((error as? URLError)?.code, .notConnectedToInternet)
      }
    }
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)),
                   ["https://smoke-app-default-rtdb.firebaseio.com/.json",
                    "https://smoke-app-default-rtdb.europe-west1.firebasedatabase.app/.json"])
    LoopbackGuard.forget(LoopbackGuard.blocked.count - blockedBefore)
    // The database itself is on this machine: its host is the generated URL's.
    XCTAssertEqual(URL(string: db.reference().url)?.host, "127.0.0.1")
  }

  // MARK: Helpers

  /// Records the events of one observer: value, childAdded, childRemoved, ... and cancelled.
  final class Recorder {
    struct Event {
      let kind: String
      let key: String?
      let prev: String?
      let value: Any?
      let keys: [String]
      let error: NSError?
    }

    private let lock = NSLock()
    private var list: [Event] = []

    var events: [Event] {
      lock.lock()
      defer { lock.unlock() }
      return list
    }

    func add(_ e: Event) {
      lock.lock()
      list.append(e)
      lock.unlock()
    }
  }

  private func listen(_ q: DatabaseQuery, _ type: DataEventType = .value) -> Recorder {
    let r = Recorder()
    let kind = type == .value ? "value" : "child"
    let h = q.observe(type, andPreviousSiblingKeyWith: { snap, prev in
      let keys = (snap.children.allObjects as? [DataSnapshot] ?? []).map(\.key)
      r.add(.init(kind: kind, key: snap.key, prev: prev, value: snap.value, keys: keys, error: nil))
    }, withCancel: { error in
      r.add(.init(kind: "cancelled", key: nil, prev: nil, value: nil, keys: [], error: error as NSError))
    })
    observers.append((q, h))
    return r
  }

  struct Timeout: Error, CustomStringConvertible {
    let what: String
    var description: String { "timed out waiting for \(what)" }
  }

  private func eventually(_ what: String, timeout: TimeInterval = 10, _ cond: () async throws -> Bool) async throws {
    let end = Date().addingTimeInterval(timeout)
    while try await !cond() {
      if Date() > end {
        throw Timeout(what: what)
      }
      try await Task.sleep(nanoseconds: 100_000_000)
    }
  }

  /// The error a write fails with, or nil when it succeeds.
  private func writeError(_ ref: DatabaseReference, _ value: Any) async -> NSError? {
    await withCheckedContinuation { cont in
      ref.setValue(value) { error, _ in cont.resume(returning: error as NSError?) }
    }
  }

  /// A write's completion, observed from the test.
  final class Completion {
    private let lock = NSLock()
    private var state: (finished: Bool, error: Error?) = (false, nil)
    var finished: Bool { lock.lock(); defer { lock.unlock() }; return state.finished }
    var error: Error? { lock.lock(); defer { lock.unlock() }; return state.error }
    func finish(_ error: Error?) { lock.lock(); state = (true, error); lock.unlock() }
  }

  private static func increment(_ ref: DatabaseReference) async throws -> Bool {
    try await withCheckedThrowingContinuation { cont in
      ref.runTransactionBlock({ data in
        data.value = ((data.value as? NSNumber)?.intValue ?? 0) + 1
        return TransactionResult.success(withValue: data)
      }, andCompletionBlock: { error, committed, _ in
        if let error {
          cont.resume(throwing: error)
        } else {
          cont.resume(returning: committed)
        }
      })
    }
  }

  // The second party: the database's REST API with an admin credential (the CLI token, which
  // bypasses the rules as a service account does), the admin Auth API and the server's switch.

  struct Rest {
    let status: Int
    let body: String
    let etag: String?
    var json: Any? { try? JSONSerialization.jsonObject(with: Data(body.utf8), options: [.fragmentsAllowed]) }
  }

  private func send(_ req: URLRequest) async throws -> Rest {
    var req = req
    req.setValue("Bearer \(Self.env["ARUMA_CLI_TOKEN"] ?? "")", forHTTPHeaderField: "Authorization")
    let (data, response) = try await URLSession.shared.data(for: req)
    let http = try XCTUnwrap(response as? HTTPURLResponse)
    return Rest(status: http.statusCode, body: String(decoding: data, as: UTF8.self), etag: http.value(forHTTPHeaderField: "ETag"))
  }

  private func rest(_ method: String, _ path: String, _ body: String? = nil, headers: [String: String] = [:]) async throws -> Rest {
    var req = URLRequest(url: try XCTUnwrap(URL(string: "\(arumaURL)/\(path).json?ns=\(ns)")))
    req.httpMethod = method
    req.httpBody = body.map { Data($0.utf8) }
    for (k, v) in headers {
      req.setValue(v, forHTTPHeaderField: k)
    }
    return try await send(req)
  }

  /// Adds one to a number with a conditional REST write (ETag and if-match), as the Admin SDKs'
  /// transactions over REST do, retrying when another writer came first. Returns the attempts.
  private func adminIncrement(_ path: String) async throws -> Int {
    var cur = try await rest("GET", path, headers: ["X-Firebase-ETag": "true"])
    for attempt in 1...100 {
      let v = (cur.json as? NSNumber)?.intValue ?? 0
      let next = try await rest("PUT", path, "\(v + 1)", headers: ["X-Firebase-ETag": "true", "if-match": cur.etag ?? ""])
      if next.status == 200 {
        return attempt
      }
      XCTAssertEqual(next.status, 412, next.body)
      cur = next  // 412 carries the current value and its ETag
    }
    throw Timeout(what: "an increment of \(path) in 100 attempts")
  }

  /// Disables a user as firebase-admin's updateUser(uid, disabled) does: accounts:update.
  private func disableUser(_ uid: String) async throws {
    var req = URLRequest(url: try XCTUnwrap(URL(string: "\(arumaURL)/identitytoolkit.googleapis.com/v1/projects/\(projectID)/accounts:update")))
    req.httpMethod = "POST"
    req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    req.httpBody = Data(#"{"localId":"\#(uid)","disableUser":true}"#.utf8)
    let r = try await send(req)
    XCTAssertEqual(r.status, 200, r.body)
  }

  /// Takes the whole server down (every connection closed, new ones refused) or up again.
  private func server(_ state: String) async throws {
    var req = URLRequest(url: try XCTUnwrap(URL(string: "\(Self.env["ARUMA_SWITCH_URL"] ?? "")/\(state)")))
    req.httpMethod = "POST"
    let (_, response) = try await URLSession.shared.data(for: req)
    XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
  }
}
