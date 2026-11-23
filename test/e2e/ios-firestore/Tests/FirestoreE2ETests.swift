import ArumaRedirect
import FirebaseAuth
import FirebaseCore
import FirebaseFirestore
import XCTest

/// The official FirebaseFirestore for iOS in production mode: the host of the Aruma server set in
/// FirestoreSettings (the SDK reads it nowhere else; GoogleService-Info.plist carries the project),
/// signed in with FirebaseAuth through ArumaRedirect, against a real server under the
/// cloud.firestore ruleset of firestore.rules released through the firebaserules API. Two SDK
/// clients run in the process (the default app and a second one, each with its own user and
/// memory cache), and the REST API with an admin credential is the third party. URLSession
/// requests other than to this machine are refused (LoopbackGuard); Firestore's gRPC channels do
/// not go through URLSession, and run.sh checks the process's sockets for them. The scenarios are
/// android-firestore's, and the rest of what an app uses: field transforms, queries, collection
/// groups, aggregations, transactions, an offline write across a server restart, a persistent
/// cache resumed by a new instance, and a disabled user.
///
/// run.sh passes the server through TEST_RUNNER_ARUMA_URL, TEST_RUNNER_ARUMA_PROJECT_ID,
/// TEST_RUNNER_ARUMA_CLI_TOKEN, TEST_RUNNER_ARUMA_SWITCH_URL (the server's off switch) and
/// TEST_RUNNER_ARUMA_RULESET (the name of the released firestore.rules ruleset).
final class FirestoreE2ETests: XCTestCase {
  private static let env = ProcessInfo.processInfo.environment
  private var arumaURL: String { Self.env["ARUMA_URL"] ?? "" }
  private var projectID: String { Self.env["ARUMA_PROJECT_ID"] ?? "" }
  /// The Firestore host the SDK is given: host:port of ARUMA_URL.
  private static var host: String {
    guard let url = URL(string: env["ARUMA_URL"] ?? ""), let h = url.host, let p = url.port else { return "" }
    return "\(h):\(p)"
  }
  /// Tells this run's documents and users apart from an earlier run's on the same server.
  private static let run = String(UInt64(Date().timeIntervalSince1970 * 1000), radix: 36)

  private var auth: Auth { Auth.auth() }
  private var db: Firestore { Firestore.firestore() }
  /// The second client: another FirebaseApp with the same options, its own user and connection.
  private static var second: FirebaseApp!
  private var auth2: Auth { Auth.auth(app: Self.second) }
  private var db2: Firestore { Firestore.firestore(app: Self.second) }

  private var blockedBefore = 0
  private var recorders: [Recorder] = []

  /// Settings in production mode: Aruma's host, cleartext on loopback, and a memory cache (the
  /// resume token a listener keeps across disableNetwork and enableNetwork is then the SDK's, not
  /// a database's) unless persistent.
  static func settings(persistent: Bool) -> FirestoreSettings {
    let s = FirestoreSettings()
    s.host = host
    s.isSSLEnabled = false
    if persistent {
      s.cacheSettings = PersistentCacheSettings()
    } else {
      s.cacheSettings = MemoryCacheSettings()
    }
    return s
  }

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
    // Before the first use of either instance: settings cannot change once it started.
    guard !host.isEmpty, let other = second else { return }
    Firestore.firestore().settings = settings(persistent: false)
    Firestore.firestore(app: other).settings = settings(persistent: false)
  }

  override class func tearDown() {
    let refused = LoopbackGuard.blocked
    print("ARUMA_E2E guard: \(LoopbackGuard.loopbackPaths.count) requests to Aruma, "
      + "\(LoopbackGuard.redirected.count) Auth requests to Google hosts redirected by ArumaRedirect, "
      + "\(refused.count) refused \(refused)")
    super.tearDown()
  }

  override func setUp() async throws {
    struct NotConfigured: Error, CustomStringConvertible {
      var description: String { "run through test/e2e/ios-firestore/run.sh (ARUMA_URL is not set)" }
    }
    guard !arumaURL.isEmpty else { throw NotConfigured() }
    blockedBefore = LoopbackGuard.blocked.count
    try auth.signOut()
    try auth2.signOut()
    try await db.enableNetwork()
    try await db2.enableNetwork()
  }

  override func tearDown() async throws {
    for r in recorders {
      r.stop()
    }
    recorders = []
    try? await db.enableNetwork()
    try? await db2.enableNetwork()
    // The guard: nothing but this machine was asked for during the test.
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)), [], "requests leaving the machine")
  }

  // MARK: 1. Production mode, sign-in, and the rules (android-firestore 1).

  func testRulesAdmitTheSignedInAndRefuseTheRest() async throws {
    XCTAssertTrue(ArumaRedirect.loaderRan)
    XCTAssertEqual(ArumaRedirect.baseURL?.absoluteString, arumaURL)
    XCTAssertEqual(FirebaseApp.app()?.options.projectID, projectID)
    XCTAssertEqual(db.settings.host, Self.host)
    XCTAssertFalse(db.settings.isSSLEnabled)
    XCTAssertEqual(db2.settings.host, Self.host)
    XCTAssertEqual(Self.codeNames[FirestoreErrorCode.permissionDenied.rawValue], "permissionDenied")
    XCTAssertEqual(Self.codeNames[FirestoreErrorCode.unauthenticated.rawValue], "unauthenticated")
    for name in ["FIRInstallations", "FIRAnalytics", "FIRMessaging"] {
      XCTAssertNil(NSClassFromString(name), "\(name) is linked in")
    }
    let d = db.collection("e2e").document("rules-\(Self.run)")
    // Signed out: the rules say no, to a write, a get and a listener alike.
    var e = await failure { try await d.setData(["a": 1]) }
    XCTAssertEqual(e, "permissionDenied")
    e = await failure { _ = try await d.getDocument(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
    let denied = listen(d)
    try await eventually("denied") { denied.events.count == 1 }
    XCTAssertEqual(denied.last, "error permissionDenied")
    // A list query and a query listener are judged too, not only single documents.
    let all = db.collection("e2e")
    e = await failure { _ = try await all.getDocuments(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
    let deniedQuery = listen(all)
    try await eventually("denied query") { deniedQuery.events.count == 1 }
    XCTAssertEqual(deniedQuery.last, "error permissionDenied")

    let uid = try await auth.signInAnonymously().user.uid
    XCTAssertEqual(uid.count, 28)
    try await d.setData(["a": 1])
    let read = try await d.getDocument(source: .server)
    XCTAssertEqual(read.data().map(Self.render), "{a=1}")
    XCTAssertFalse(read.metadata.isFromCache)
    // Outside the rules' paths even a signed-in user is refused.
    e = await failure { try await self.db.collection("private").document("x").setData(["a": 1]) }
    XCTAssertEqual(e, "permissionDenied")
    // The admin (the CLI token) is not subject to the rules and sees what the SDK wrote.
    let admin = try await document("e2e/rules-\(Self.run)")
    XCTAssertEqual(Self.field(admin, "a", "integerValue"), "1")
  }

  // MARK: 1b. Rules with get(), exists() and request.auth.uid, between two users.

  func testRulesWithGetExistsAndOwnership() async throws {
    let ua = try await auth.signInAnonymously().user.uid
    let ub = try await auth2.signInAnonymously().user.uid
    let room = "r-\(Self.run)"
    // A room is created by its owner only, and read by its owner only.
    var e = await failure { try await self.db.document("rooms/\(room)").setData(["owner": ub, "name": "x"]) }
    XCTAssertEqual(e, "permissionDenied")
    try await db.document("rooms/\(room)").setData(["owner": ua, "name": "ops"])
    e = await failure { _ = try await self.db2.document("rooms/\(room)").getDocument(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
    let mine = try await db.document("rooms/\(room)").getDocument(source: .server)
    XCTAssertEqual(mine.get("owner") as? String, ua)

    // Members are written by the room's owner only: get() of the room.
    let member = "rooms/\(room)/members/\(ub)"
    e = await failure { try await self.db2.document(member).setData(["role": "member"]) }
    XCTAssertEqual(e, "permissionDenied")
    // Messages need membership: exists() of the member's document.
    let messages = db2.collection("rooms/\(room)/messages")
    e = await failure { try await messages.document("m1").setData(["text": "hi"]) }
    XCTAssertEqual(e, "permissionDenied")
    e = await failure { _ = try await messages.getDocuments(source: .server) }
    XCTAssertEqual(e, "permissionDenied")

    try await db.document(member).setData(["role": "member"])
    try await messages.document("m1").setData(["text": "مرحبا", "from": ub])
    let read = try await messages.getDocuments(source: .server)
    XCTAssertEqual(read.documents.map(\.documentID), ["m1"])
    // The owner is not a member: owning the room does not open its messages.
    e = await failure { _ = try await self.db.collection("rooms/\(room)/messages").getDocuments(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
    let stored = try await document("rooms/\(room)/messages/m1")
    XCTAssertEqual(Self.field(stored, "text", "stringValue"), "مرحبا")

    // Membership taken away: the next write is refused.
    try await db.document(member).delete()
    e = await failure { try await messages.document("m2").setData(["text": "again"]) }
    XCTAssertEqual(e, "permissionDenied")

    // Each user's own documents (request.auth.uid == uid): B's are B's.
    let note = "users/\(ub)/notes/n"
    try await db2.document(note).setData(["v": 1])
    e = await failure { _ = try await self.db.document(note).getDocument(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
    e = await failure { _ = try await self.db.collection("users/\(ub)/notes").getDocuments(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
    e = await failure { try await self.db.document(note).setData(["v": 2]) }
    XCTAssertEqual(e, "permissionDenied")
    let own = try await db2.document(note).getDocument(source: .server)
    XCTAssertEqual((own.get("v") as? NSNumber)?.intValue, 1)
  }

  // MARK: 1c. A release published while a listener is open takes the listener away (android-firestore 1c).

  func testDenyAllReleaseRemovesAnOpenListener() async throws {
    let original = Self.env["ARUMA_RULESET"] ?? ""
    XCTAssertFalse(original.isEmpty, "run through run.sh (no ARUMA_RULESET)")
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let col = "r-\(Self.run)"
    let q = db.collection("e2e").document(col).collection("items")
    let a = db2.collection("e2e").document(col).collection("items").document("a")
    let r = listen(q)
    try await eventually("empty") { r.events.count == 1 }
    r.clear()
    try await a.setData(["n": 1])
    try await eventually("a enters") { r.events.contains("[ADDED a {n=1}]") }
    r.clear()
    let denyAll = try await createRuleset("rules_version = '2';\nservice cloud.firestore { match /databases/{d}/documents { match /{p=**} { allow read, write: if false; } } }\n")
    try await publish(denyAll)
    do {
      // The server judges the open listener again against the new release and ends it.
      try await eventually("removed", timeout: 15) { r.events.contains("error permissionDenied") }
      XCTAssertEqual(r.last, "error permissionDenied")
      let e = await failure { _ = try await a.getDocument(source: .server) }
      XCTAssertEqual(e, "permissionDenied")
    } catch {
      try await publish(original)
      throw error
    }
    try await publish(original)
    // The first ruleset is back: the same client reads again.
    let back = try await a.getDocument(source: .server)
    XCTAssertEqual((back.get("n") as? NSNumber)?.intValue, 1)
  }

  // MARK: 2. A document listener sees this client's own writes and field transforms (android-firestore 2, 3).

  func testDocumentListenerSeesTheWritesAndTransformsOfItsClient() async throws {
    _ = try await auth.signInAnonymously()
    let d = db.collection("e2e").document("doc-\(Self.run)")
    let r = listen(d, metadata: true)
    // Nothing there: the server says so, and that is the first snapshot not from the cache.
    try await eventually("missing") { r.events.contains("missing") }
    r.clear()

    let t0 = Date().timeIntervalSince1970 * 1000
    try await d.setData(["name": "سارة", "count": 1, "at": FieldValue.serverTimestamp()])
    // The local event has the time still unset; the acknowledged one has the server's.
    try await eventually("acknowledged") { r.events.contains { $0.contains("at=@") && !$0.contains("pending") } }
    XCTAssertEqual(r.events.first, "{at=null, count=1, name=سارة} pending")
    let acked = try XCTUnwrap(r.events.last { $0.contains("at=@") && !$0.contains("pending") })
    XCTAssertTrue(Self.matches(acked, #"\{at=@\d+, count=1, name=سارة\}"#), acked)
    let at = try XCTUnwrap(Self.millis(acked))
    XCTAssertLessThan(abs(at - t0), 60_000, "server time \(at) near \(t0)")
    r.clear()

    try await d.updateData(["count": FieldValue.increment(Int64(5))])
    try await eventually("six") { r.last.map { $0.contains("count=6") && !$0.contains("pending") } ?? false }
    for event in r.events {
      XCTAssertTrue(Self.matches(event, #"\{at=@\d+, count=6, name=سارة\}( pending)?"#), event)
    }
    let six = try await d.getDocument(source: .server)
    XCTAssertEqual((six.get("count") as? NSNumber)?.int64Value, 6)
    // The admin credential sees the increment's result.
    let fields = try await document("e2e/doc-\(Self.run)")
    XCTAssertEqual(Self.field(fields, "count", "integerValue"), "6")
    r.clear()

    // arrayUnion adds what is missing only, arrayRemove takes every equal element out, and
    // FieldValue.delete() removes a field, in one update.
    try await d.updateData(["tags": FieldValue.arrayUnion(["a", "b"])])
    try await d.updateData(["tags": FieldValue.arrayUnion(["b", "c"])])
    try await d.updateData(["tags": FieldValue.arrayRemove(["a"]), "name": FieldValue.delete()])
    try await eventually("array transforms") {
      r.last.map { Self.matches($0, #"\{at=@\d+, count=6, tags=\[b, c\]\}"#) } ?? false
    }
    let transformed = try await document("e2e/doc-\(Self.run)")
    let tags = ((transformed?["fields"] as? [String: Any])?["tags"] as? [String: Any])?["arrayValue"] as? [String: Any]
    XCTAssertEqual((tags?["values"] as? [[String: Any]])?.compactMap { $0["stringValue"] as? String }, ["b", "c"])
    XCTAssertNil(Self.field(transformed, "name", "stringValue"))
    r.clear()

    // A delete is seen by the listener.
    try await d.delete()
    try await eventually("deleted") { r.events.contains("missing") }
    XCTAssertEqual(r.last, "missing")
    let gone = try await d.getDocument(source: .server)
    XCTAssertFalse(gone.exists)
  }

  // MARK: 3. A query listener, and another client's writes entering, changing and leaving it (android-firestore 1b).

  func testQueryListenerFollowsAnotherClientsWrites() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let col = "q-\(Self.run)"
    let q = db.collection("e2e").document(col).collection("items").whereField("n", isGreaterThan: 1).order(by: "n")
    let r = listen(q)
    try await eventually("empty") { r.events.count == 1 }
    XCTAssertEqual(r.last, "[]")
    r.clear()

    let a = db2.collection("e2e").document(col).collection("items").document("a")
    let b = db2.collection("e2e").document(col).collection("items").document("b")
    try await a.setData(["n": 5])
    try await eventually("a enters") { r.events.count == 1 }
    XCTAssertEqual(r.last, "[ADDED a {n=5}]")
    try await b.setData(["n": 1])  // below the filter: not seen
    try await b.updateData(["n": 3])  // enters
    try await eventually("b enters") { r.events.count == 2 }
    XCTAssertEqual(r.last, "[ADDED b {n=3}]")
    try await a.updateData(["n": 2])  // changes, and moves before b
    try await eventually("a changes") { r.events.count == 3 }
    XCTAssertEqual(r.last, "[MODIFIED a {n=2}]")
    XCTAssertEqual(r.ids, ["a", "b"])
    try await b.updateData(["n": 0])  // leaves
    try await eventually("b leaves") { r.events.count == 4 }
    XCTAssertEqual(r.last, "[REMOVED b {n=3}]")
    try await a.delete()
    try await eventually("a deleted") { r.events.count == 5 }
    XCTAssertEqual(r.last, "[REMOVED a {n=2}]")
    let now = try await q.getDocuments(source: .server)
    XCTAssertEqual(now.documents.map(\.documentID), [])
  }

  // MARK: 4. Two FirebaseApps: each one's writes reach the other in order, with the metadata of who wrote them.

  func testTwoClientsSeeEachOthersWritesInOrderWithMetadata() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let path = "e2e/chat-\(Self.run)/messages"
    let a = listen(db.collection(path).order(by: "seq"), metadata: true)
    let b = listen(db2.collection(path).order(by: "seq"), metadata: true)
    // The server's answer (an empty result not from the cache) at both.
    try await eventually("both synced") { a.events.contains("[]") && b.events.contains("[]") }
    a.clear()
    b.clear()

    for i in 1...5 {
      try await db.collection(path).document("m\(i)").setData(["seq": i, "from": "a", "at": FieldValue.serverTimestamp()])
    }
    try await db2.collection(path).document("m6").setData(["seq": 6, "from": "b", "at": FieldValue.serverTimestamp()])
    let all = ["m1", "m2", "m3", "m4", "m5", "m6"]
    func synced(_ r: Recorder) -> Bool {
      r.ids == all && (r.last.map { !$0.contains("pending") && !$0.contains("cache") } ?? false)
    }
    try await eventually("all six at both, synced", timeout: 15) { synced(a) && synced(b) && a.merged == b.merged }

    // B saw A's messages once each, in the order A wrote them, already committed: with the
    // server's time and no pending writes; never from the cache.
    let fromA = b.changes.filter { ["m1", "m2", "m3", "m4", "m5"].contains($0.id) }
    XCTAssertEqual(fromA.map { "\($0.kind) \($0.id) \($0.pending)" },
                   ["ADDED m1 false", "ADDED m2 false", "ADDED m3 false", "ADDED m4 false", "ADDED m5 false"])
    for c in fromA {
      XCTAssertTrue(Self.matches(c.data, #"\{at=@\d+, from=a, seq=\d\}"#), c.data)
    }
    XCTAssertFalse(b.events.contains { $0.hasSuffix(" cache") }, "\(b.events)")
    // Each client saw its own write first as pending with the time unset, last as committed
    // with the time the other client sees.
    for (r, id, from, seq) in [(a, "m1", "a", 1), (a, "m5", "a", 5), (b, "m6", "b", 6)] {
      let own = r.changes.filter { $0.id == id }
      XCTAssertEqual(own.first.map { "\($0.kind) \($0.data) \($0.pending)" }, "ADDED {at=null, from=\(from), seq=\(seq)} true")
      XCTAssertEqual(own.last?.pending, false)
      XCTAssertTrue(own.last.map { Self.matches($0.data, #"\{at=@\d+, from=\#(from), seq=\#(seq)\}"#) } ?? false, "\(own)")
    }
    // The server's times follow the order of the writes.
    let times = all.compactMap { id in b.changes.last { $0.id == id }.flatMap { Self.millis($0.data) } }
    XCTAssertEqual(times.count, 6)
    XCTAssertEqual(times, times.sorted())
  }

  // MARK: 5. get() from the server, of a document and of a query (android-firestore 4).

  func testGetFromTheServer() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let col = "g-\(Self.run)"
    let theirs = db2.collection("e2e").document(col).collection("items").document("x")
    try await theirs.setData(["n": 7, "tags": ["a", "b"], "nested": ["k": true], "pi": 3.5, "none": NSNull()])
    let mine = db.collection("e2e").document(col).collection("items").document("x")
    let s = try await mine.getDocument(source: .server)
    XCTAssertTrue(s.exists)
    XCTAssertFalse(s.metadata.isFromCache)
    XCTAssertEqual(s.data().map(Self.render), "{n=7, nested={k=true}, none=null, pi=3.5, tags=[a, b]}")
    let qs = try await db.collection("e2e").document(col).collection("items").order(by: "n").getDocuments(source: .server)
    XCTAssertEqual(qs.documents.map(\.documentID), ["x"])
    XCTAssertFalse(qs.metadata.isFromCache)
    let none = try await db.collection("e2e").document("\(col)-none").getDocument(source: .server)
    XCTAssertFalse(none.exists)
  }

  // MARK: 6. Queries: filters, order, limits and cursors, in, not-in, array-contains(-any).

  func testQueriesFiltersOrderLimitsAndCursors() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let path = "e2e/qs-\(Self.run)/items"
    let seed: [(String, Int, String, [String])] = [
      ("p0", 50, "Riyadh", ["red"]), ("p1", 10, "Cairo", ["blue", "green"]), ("p2", 80, "Tunis", ["green"]),
      ("p3", 30, "Riyadh", []), ("p4", 70, "Amman", ["red", "blue"]), ("p5", 20, "Cairo", ["yellow"]),
      ("p6", 60, "Rabat", ["green", "red"]), ("p7", 40, "Tunis", ["blue"]),
    ]
    let batch = db2.batch()
    for (id, n, city, tags) in seed {
      batch.setData(["n": n, "city": city, "tags": tags], forDocument: db2.collection(path).document(id))
    }
    try await batch.commit()

    let items = db.collection(path)
    func ids(_ q: Query) async throws -> [String] {
      try await q.getDocuments(source: .server).documents.map(\.documentID)
    }
    let page = items.whereField("n", isGreaterThanOrEqualTo: 30).order(by: "n").limit(to: 3)
    let first = try await page.getDocuments(source: .server)
    XCTAssertEqual(first.documents.map(\.documentID), ["p3", "p7", "p0"])
    let lastOfFirst = try XCTUnwrap(first.documents.last)
    var got = try await ids(page.start(afterDocument: lastOfFirst))
    XCTAssertEqual(got, ["p6", "p4", "p2"])
    got = try await ids(items.order(by: "n").limit(toLast: 2))
    XCTAssertEqual(got, ["p4", "p2"])
    got = try await ids(items.order(by: "n").start(at: [20]).end(before: [50]))
    XCTAssertEqual(got, ["p5", "p3", "p7"])
    got = try await ids(items.order(by: "n").start(after: [20]).end(at: [50]))
    XCTAssertEqual(got, ["p3", "p7", "p0"])
    got = try await ids(items.order(by: "n", descending: true).limit(to: 2))
    XCTAssertEqual(got, ["p2", "p4"])
    got = try await ids(items.whereField("city", isEqualTo: "Tunis"))
    XCTAssertEqual(got, ["p2", "p7"])
    got = try await ids(items.whereField("city", in: ["Riyadh", "Cairo"]))
    XCTAssertEqual(got, ["p0", "p1", "p3", "p5"])
    got = try await ids(items.whereField("n", notIn: [10, 80]))
    XCTAssertEqual(got, ["p5", "p3", "p7", "p0", "p6", "p4"])
    got = try await ids(items.whereField("tags", arrayContains: "green"))
    XCTAssertEqual(got, ["p1", "p2", "p6"])
    got = try await ids(items.whereField("tags", arrayContainsAny: ["blue", "yellow"]))
    XCTAssertEqual(got, ["p1", "p4", "p5", "p7"])

    // A limited query as a listener: a document of the other client enters the window, the
    // lowest leaves it.
    let top = listen(items.order(by: "n", descending: true).limit(to: 3))
    try await eventually("the top three") { top.ids == ["p2", "p4", "p6"] }
    top.clear()
    try await db2.collection(path).document("p8").setData(["n": 75, "city": "Doha", "tags": [String]()])
    try await eventually("p8 in, p6 out") { top.ids == ["p2", "p8", "p4"] }
    XCTAssertEqual(top.last, "[REMOVED p6 {city=Rabat, n=60, tags=[green, red]}, ADDED p8 {city=Doha, n=75, tags=[]}]")
  }

  // MARK: 7. Collection-group queries, and one the rules do not cover.

  func testCollectionGroupQueries() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let base = "e2e/cg-\(Self.run)"
    let landmarks = ["\(base)/cities/a/landmarks/x1", "\(base)/cities/b/landmarks/x2", "\(base)/landmarks/x3"]
    let batch = db.batch()
    for p in landmarks + ["\(base)/cities/a/landmarksX/y", "\(base)/cities/a/notes/z"] {
      batch.setData(["p": p], forDocument: db.document(p))
    }
    try await batch.commit()

    let group = try await db.collectionGroup("landmarks").getDocuments(source: .server)
    XCTAssertEqual(group.documents.map(\.reference.path).filter { $0.hasPrefix(base + "/") }.sorted(), landmarks)
    // Bounded to this run's documents by their full path (__name__): the cursor form of a prefix.
    let bounded = db.collectionGroup("landmarks").order(by: FieldPath.documentID())
      .start(at: [base]).end(at: [base + "\u{f8ff}"])
    let exact = try await bounded.getDocuments(source: .server)
    XCTAssertEqual(exact.documents.map(\.reference.path), landmarks)
    // As a listener: a landmark the other client adds elsewhere under the run enters it.
    let r = listen(bounded)
    try await eventually("three") { r.ids == ["x1", "x2", "x3"] }
    r.clear()
    try await db2.document("\(base)/cities/c/landmarks/x4").setData(["p": "x4"])
    try await eventually("x4 enters") { r.ids == ["x1", "x2", "x4", "x3"] }
    XCTAssertEqual(r.last, "[ADDED x4 {p=x4}]")
    // No rule matches every notes collection: the group query is refused.
    let e = await failure { _ = try await self.db.collectionGroup("notes").getDocuments(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
  }

  // MARK: 8. Aggregations: count, sum and average, judged by the rules like their query.

  func testCountSumAndAverageAggregations() async throws {
    _ = try await auth.signInAnonymously()
    let items = db.collection("e2e/agg-\(Self.run)/items")
    let batch = db.batch()
    for i in 1...6 {
      batch.setData(["n": i], forDocument: items.document("i\(i)"))
    }
    try await batch.commit()
    let all = try await items.count.getAggregation(source: .server)
    XCTAssertEqual(all.count.intValue, 6)
    let some = try await items.whereField("n", isGreaterThan: 2).count.getAggregation(source: .server)
    XCTAssertEqual(some.count.intValue, 4)
    let empty = try await db.collection("e2e/agg-\(Self.run)/none").count.getAggregation(source: .server)
    XCTAssertEqual(empty.count.intValue, 0)
    let agg = try await items.aggregate([AggregateField.count(), AggregateField.sum("n"), AggregateField.average("n")])
      .getAggregation(source: .server)
    XCTAssertEqual((agg.get(AggregateField.count()) as? NSNumber)?.intValue, 6)
    XCTAssertEqual((agg.get(AggregateField.sum("n")) as? NSNumber)?.intValue, 21)
    XCTAssertEqual((agg.get(AggregateField.average("n")) as? NSNumber)?.doubleValue, 3.5)
    // Signed out, the count is refused as the query would be.
    try auth.signOut()
    let e = await failure { _ = try await items.count.getAggregation(source: .server) }
    XCTAssertEqual(e, "permissionDenied")
  }

  // MARK: 9. A WriteBatch lands in one snapshot; a batch that fails a rule changes nothing (android-firestore 5).

  func testWriteBatchLandsAtOnce() async throws {
    _ = try await auth.signInAnonymously()
    let items = db.collection("e2e").document("b-\(Self.run)").collection("items")
    try await items.document("upd").setData(["v": 1])
    try await items.document("del").setData(["v": 1])
    let r = listen(items.order(by: FieldPath.documentID()))
    try await eventually("initial") { r.events.count == 1 }
    XCTAssertEqual(r.last, "[ADDED del {v=1}, ADDED upd {v=1}]")
    r.clear()

    let batch = db.batch()
    batch.setData(["v": 1, "at": FieldValue.serverTimestamp()], forDocument: items.document("new"))
    batch.updateData(["v": FieldValue.increment(Int64(10))], forDocument: items.document("upd"))
    batch.deleteDocument(items.document("del"))
    try await batch.commit()
    try await eventually("batch") { r.events.contains { $0.contains("at=@") } }
    // The three writes are one change set, however many snapshots the SDK raised for the
    // pending state and the acknowledgement.
    let events = r.events
    XCTAssertEqual(events.first, "[REMOVED del {v=1}, ADDED new {at=null, v=1}, MODIFIED upd {v=11}]")
    XCTAssertTrue(Self.matches(events.last ?? "", #"\[MODIFIED new \{at=@\d+, v=1\}\]"#), "\(events)")
    let server = try await items.order(by: FieldPath.documentID()).getDocuments(source: .server)
    XCTAssertEqual(server.documents.map(\.documentID), ["new", "upd"])
    let upd = try await items.document("upd").getDocument(source: .server)
    XCTAssertEqual((upd.get("v") as? NSNumber)?.intValue, 11)
    let del = try await items.document("del").getDocument(source: .server)
    XCTAssertFalse(del.exists)

    // Its second write is outside the rules: the first does not land either.
    let bad = db.batch()
    bad.setData(["v": 1], forDocument: items.document("never"))
    bad.setData(["v": 1], forDocument: db.collection("private").document("y"))
    let e = await failure { try await bad.commit() }
    XCTAssertEqual(e, "permissionDenied")
    let never = try await items.document("never").getDocument(source: .server)
    XCTAssertFalse(never.exists)
  }

  // MARK: 10. Transactions from both clients on the same documents: none lost, none applied twice.

  func testConcurrentTransactionsKeepTheSum() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let base = "e2e/tx-\(Self.run)"
    try await db.document("\(base)/accounts/a").setData(["v": 100])
    try await db.document("\(base)/accounts/b").setData(["v": 0])
    let attempts = Counter()
    let n = 6
    let clients = [db, db2]
    let outcomes = await withTaskGroup(of: String.self) { group -> [String] in
      for _ in 0..<n {
        for client in clients {
          group.addTask { await Self.move(client, base, attempts) }
        }
      }
      var out: [String] = []
      for await o in group {
        out.append(o)
      }
      return out
    }
    let committed = outcomes.filter { $0 == "committed" }.count
    print("ARUMA_E2E transactions: \(committed) of \(2 * n) committed in \(attempts.value) attempts; "
      + "the others: \(outcomes.filter { $0 != "committed" })")
    XCTAssertGreaterThan(committed, 0)
    for o in outcomes where o != "committed" {
      // Contention may exhaust the SDK's 5 attempts; nothing else may fail.
      XCTAssertTrue(["failed aborted", "failed failedPrecondition"].contains(o), o)
    }
    let a = try await db.document("\(base)/accounts/a").getDocument(source: .server)
    let b = try await db2.document("\(base)/accounts/b").getDocument(source: .server)
    let va = (a.get("v") as? NSNumber)?.intValue ?? -1
    let vb = (b.get("v") as? NSNumber)?.intValue ?? -1
    XCTAssertEqual(va + vb, 100)
    XCTAssertEqual(vb, committed)

    // A transaction whose write is outside the rules fails with permission-denied, not retried.
    let outside = db.collection("private").document("t-\(Self.run)")
    do {
      _ = try await db.runTransaction { transaction, _ -> Any? in
        transaction.setData(["v": 1], forDocument: outside)
        return nil
      }
      XCTFail("a transaction outside the rules committed")
    } catch {
      XCTAssertEqual(Self.describe(error), "permissionDenied")
    }
  }

  // MARK: 11. disableNetwork and enableNetwork: the listener resumes with its token and catches up (android-firestore 6).

  func testListenerResumesAfterDisableAndEnableNetwork() async throws {
    _ = try await auth.signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let col = "o-\(Self.run)"
    let mine = db.collection("e2e").document(col).collection("items")
    let theirs = db2.collection("e2e").document(col).collection("items")
    try await mine.document("keep").setData(["v": 1])
    try await mine.document("gone").setData(["v": 1])
    try await mine.document("edit").setData(["v": 1])
    let r = listen(mine.order(by: FieldPath.documentID()))
    try await eventually("initial") { r.events.count == 1 }
    XCTAssertEqual(r.last, "[ADDED edit {v=1}, ADDED gone {v=1}, ADDED keep {v=1}]")
    r.clear()

    try await db.disableNetwork()
    // While this client is away the other one edits, deletes and adds.
    try await theirs.document("edit").setData(["v": 2])
    try await theirs.document("gone").delete()
    try await theirs.document("added").setData(["v": 1])
    try await Task.sleep(nanoseconds: 1_500_000_000)
    XCTAssertEqual(r.events, [], "nothing arrives while offline")
    // A write made offline is applied locally at once, and only waits for the network.
    let offline = listen(mine.document("offline"))
    try await eventually("missing") { offline.events.contains("missing cache") }
    let done = Completion()
    mine.document("offline").setData(["v": 9]) { error in done.finish(error) }
    try await eventually("local write") { offline.events.contains("{v=9} pending") }
    try await Task.sleep(nanoseconds: 500_000_000)
    XCTAssertFalse(done.finished, "the write waits for the network")
    r.clear()

    try await db.enableNetwork()
    try await eventually("the offline write", timeout: 30) { done.finished }
    XCTAssertNil(done.error)
    try await eventually("caught up", timeout: 15) { r.merged == "added {v=1}, edit {v=2}, keep {v=1}, offline {v=9}" }
    // What arrived was the other client's work plus this one's own write.
    XCTAssertTrue(r.events.contains { $0.contains("REMOVED gone {v=1}") }, "\(r.events)")
    let server = try await mine.order(by: FieldPath.documentID()).getDocuments(source: .server)
    XCTAssertEqual(server.documents.map(\.documentID), ["added", "edit", "keep", "offline"])
    // And the listener is live again.
    r.clear()
    try await theirs.document("keep").setData(["v": 3])
    try await eventually("live") { r.events.contains("[MODIFIED keep {v=3}]") }
  }

  // MARK: 12. Offline: with the server down, a write is applied locally and lands when it is back.

  func testOfflineWriteLandsWhenTheServerIsBack() async throws {
    let uid = try await auth.signInAnonymously().user.uid
    _ = try await auth2.signInAnonymously()
    let path = "e2e/sw-\(Self.run)/items"
    let items = db.collection(path)
    try await items.document("n0").setData(["v": 0])
    let r = listen(items.order(by: FieldPath.documentID()), metadata: true)
    try await eventually("synced") { r.ids == ["n0"] && (r.last.map { !$0.contains("cache") && !$0.contains("pending") } ?? false) }

    let done = Completion()
    try await server("down")
    do {
      // The SDK notices the server is gone and says its results are from the cache.
      try await eventually("offline", timeout: 30) { r.last?.hasSuffix(" cache") ?? false }
      items.document("n1").setData(["v": 1, "by": uid, "at": FieldValue.serverTimestamp()]) { error in done.finish(error) }
      try await eventually("the local event", timeout: 5) { r.ids == ["n0", "n1"] }
      XCTAssertTrue(r.changes.contains { $0.id == "n1" && $0.kind == "ADDED" && $0.pending && $0.data == "{at=null, by=\(uid), v=1}" },
                    "\(r.changes)")
      try await Task.sleep(nanoseconds: 3_000_000_000)
      XCTAssertFalse(done.finished, "the write completed with the server down")
    } catch {
      try await server("up")
      throw error
    }
    let up = Date().timeIntervalSince1970 * 1000
    try await server("up")
    let t0 = Date()
    // The SDK reconnects by itself (gRPC's backoff and Firestore's) and sends the queued write.
    try await eventually("the write landed", timeout: 120) { done.finished }
    print("ARUMA_E2E offline: the write landed \(Int(Date().timeIntervalSince(t0) * 1000)) ms after the server came back")
    XCTAssertNil(done.error)
    try await eventually("synced again", timeout: 30) { r.last.map { !$0.contains("cache") && !$0.contains("pending") } ?? false }
    let seen = try await db2.document("\(path)/n1").getDocument(source: .server)
    XCTAssertEqual((seen.get("v") as? NSNumber)?.intValue, 1)
    // The server's time is when the write landed, not when it was made.
    let at = try XCTUnwrap(seen.get("at") as? Timestamp)
    XCTAssertGreaterThanOrEqual(at.dateValue().timeIntervalSince1970 * 1000, up - 2000)
  }

  // MARK: 13. A persistent cache: a new instance starts from the disk and resumes from the server.

  func testPersistentCacheResumesInANewInstance() async throws {
    let name = "persist-\(Self.run)"
    if FirebaseApp.app(name: name) == nil, let options = FirebaseApp.app()?.options {
      FirebaseApp.configure(name: name, options: options)
    }
    let app = try XCTUnwrap(FirebaseApp.app(name: name))
    var cached = Firestore.firestore(app: app)
    cached.settings = Self.settings(persistent: true)
    _ = try await Auth.auth(app: app).signInAnonymously()
    _ = try await auth2.signInAnonymously()
    let path = "e2e/pc-\(Self.run)/items"
    let seed = db2.batch()
    for i in 0..<5 {
      seed.setData(["v": i], forDocument: db2.collection(path).document("d\(i)"))
    }
    try await seed.commit()

    let before = listen(cached.collection(path).order(by: FieldPath.documentID()), metadata: true)
    try await eventually("from the server") {
      before.ids == ["d0", "d1", "d2", "d3", "d4"] && (before.last.map { !$0.contains("cache") } ?? false)
    }
    before.stop()
    try await cached.terminate()
    // While no instance runs, the other client changes the collection.
    try await db2.collection(path).document("d1").setData(["v": 10])
    try await db2.collection(path).document("d2").delete()
    try await db2.collection(path).document("d5").setData(["v": 5])

    // A new instance on the same disk: first what it kept, then the server's changes.
    cached = Firestore.firestore(app: app)
    cached.settings = Self.settings(persistent: true)
    let after = listen(cached.collection(path).order(by: FieldPath.documentID()), metadata: true)
    try await eventually("caught up", timeout: 30) {
      after.merged == "d0 {v=0}, d1 {v=10}, d3 {v=3}, d4 {v=4}, d5 {v=5}" && (after.last.map { !$0.contains("cache") } ?? false)
    }
    let first = try XCTUnwrap(after.events.first)
    XCTAssertEqual(first, "[ADDED d0 {v=0}, ADDED d1 {v=1}, ADDED d2 {v=2}, ADDED d3 {v=3}, ADDED d4 {v=4}] cache")
    XCTAssertTrue(after.events.contains { $0.contains("REMOVED d2 {v=2}") }, "\(after.events)")
    after.stop()
  }

  // MARK: 14. Revocation: an admin disables the user; its listeners end with permission-denied.

  func testDisablingTheUserCancelsItsListeners() async throws {
    let uid = try await auth.createUser(withEmail: "revoked-ios-fs-\(Self.run)@example.com", password: "secret1").user.uid
    let own = db.document("users/\(uid)/notes/n")
    try await own.setData(["secret": 1])
    let doc = listen(own)
    let query = listen(db.collection("e2e/rev-\(Self.run)/items"))
    try await eventually("the data") { doc.events.count == 1 && query.events.count == 1 }
    // From the server, or first from the cache, which may still hold this client's own write.
    XCTAssertTrue(doc.last?.hasPrefix("{secret=1}") ?? false, "\(doc.events)")

    let t0 = Date()
    try await disableUser(uid)
    // The server closes the streams (UNAUTHENTICATED); the SDK asks Auth for a fresh token, which
    // a disabled user does not get, so Auth signs the user out and the listens, now without a
    // user, are refused by the rules.
    try await eventually("both listeners ended", timeout: 30) {
      (doc.last?.hasPrefix("error") ?? false) && (query.last?.hasPrefix("error") ?? false)
    }
    print("ARUMA_E2E revocation: listeners ended \(Int(Date().timeIntervalSince(t0) * 1000)) ms after the user was disabled: \(doc.last ?? "") / \(query.last ?? "")")
    XCTAssertEqual(doc.last, "error permissionDenied")
    XCTAssertEqual(query.last, "error permissionDenied")
    try await eventually("signed out", timeout: 15) { self.auth.currentUser == nil }
    // Nothing was deleted: the admin still reads the document.
    let stored = try await document("users/\(uid)/notes/n")
    XCTAssertEqual(Self.field(stored, "secret", "integerValue"), "1")
  }

  // MARK: 15. The network guard.

  /// The guard refuses what ArumaRedirect does not take, Google's Firestore host included; the
  /// SDK's own channels go to the host in its settings, which run.sh checks on the sockets.
  func testGuardRefusesTheGoogleFirestoreHost() async throws {
    let refused = ["https://firestore.googleapis.com/v1/projects/smoke-app/databases/(default)/documents/e2e/x",
                   "https://firestore.googleapis.com/google.firestore.v1.Firestore/Listen/channel"]
    for raw in refused {
      let url = try XCTUnwrap(URL(string: raw))
      do {
        _ = try await URLSession(configuration: .ephemeral).data(from: url)
        XCTFail("the request was not refused")
      } catch {
        XCTAssertEqual((error as? URLError)?.code, .notConnectedToInternet)
      }
    }
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)), [
      "https://firestore.googleapis.com/v1/projects/smoke-app/databases/(default)/documents/e2e/x",
      "https://firestore.googleapis.com/google.firestore.v1.Firestore/Listen/channel",
    ])
    LoopbackGuard.forget(LoopbackGuard.blocked.count - blockedBefore)
    // Both instances are on this machine, not on firestore.googleapis.com.
    XCTAssertTrue(db.settings.host.hasPrefix("127.0.0.1:"), db.settings.host)
    XCTAssertTrue(db2.settings.host.hasPrefix("127.0.0.1:"), db2.settings.host)
  }

  // MARK: Helpers

  /// One document change of a query snapshot: its kind, the document and its own pending writes.
  struct Change: CustomStringConvertible {
    let kind: String
    let id: String
    let data: String
    let pending: Bool
    var description: String { "\(kind) \(id) \(data)\(pending ? " pending" : "")" }
  }

  /// Records what a listener delivers: one readable line per snapshot, and for queries each
  /// document change, the documents in the query's order and the documents left standing.
  final class Recorder {
    private let lock = NSLock()
    private var list: [String] = []
    private var changeList: [Change] = []
    private var order: [String] = []
    private var view: [String: String] = [:]
    var registration: ListenerRegistration?

    var events: [String] { lock.lock(); defer { lock.unlock() }; return list }
    var last: String? { events.last }
    var changes: [Change] { lock.lock(); defer { lock.unlock() }; return changeList }
    /// The document IDs of the last query snapshot, in the query's order.
    var ids: [String] { lock.lock(); defer { lock.unlock() }; return order }
    /// The documents the snapshots left standing, as "id data, ..." by ID.
    var merged: String {
      lock.lock()
      defer { lock.unlock() }
      return view.keys.sorted().map { "\($0) \(view[$0] ?? "")" }.joined(separator: ", ")
    }

    func add(_ e: String) {
      lock.lock()
      list.append(e)
      lock.unlock()
    }

    /// Forgets the lines and changes seen so far; the documents left standing stay.
    func clear() {
      lock.lock()
      list.removeAll()
      changeList.removeAll()
      lock.unlock()
    }

    func query(_ snap: QuerySnapshot, metadata: Bool) {
      // With metadata, a document whose pending writes were acknowledged shows as MODIFIED.
      let docChanges = metadata ? snap.documentChanges(includeMetadataChanges: true) : snap.documentChanges
      let now = docChanges.map { c in
        Change(kind: c.type == .added ? "ADDED" : c.type == .modified ? "MODIFIED" : "REMOVED",
               id: c.document.documentID, data: FirestoreE2ETests.render(c.document.data()),
               pending: c.document.metadata.hasPendingWrites)
      }
      var text = "[" + now.map { "\($0.kind) \($0.id) \($0.data)" }.joined(separator: ", ") + "]"
      if metadata && snap.metadata.hasPendingWrites {
        text += " pending"
      }
      if metadata && snap.metadata.isFromCache {
        text += " cache"
      }
      lock.lock()
      for c in now {
        view[c.id] = c.kind == "REMOVED" ? nil : c.data
      }
      changeList.append(contentsOf: now)
      order = snap.documents.map(\.documentID)
      list.append(text)
      lock.unlock()
    }

    func stop() {
      registration?.remove()
      registration = nil
    }
  }

  /// A document listener: "{data}", " pending" with local writes, " cache" when from the cache
  /// without them, "missing", or "error <code>".
  private func listen(_ ref: DocumentReference, metadata: Bool = false) -> Recorder {
    let r = Recorder()
    recorders.append(r)
    r.registration = ref.addSnapshotListener(includeMetadataChanges: metadata) { snap, error in
      if let error {
        r.add("error \(FirestoreE2ETests.describe(error))")
        return
      }
      guard let snap else { return }
      var text = snap.exists ? FirestoreE2ETests.render(snap.data() ?? [:]) : "missing"
      if snap.exists && snap.metadata.hasPendingWrites {
        text += " pending"
      }
      if snap.metadata.isFromCache && !snap.metadata.hasPendingWrites {
        text += " cache"
      }
      r.add(text)
    }
    return r
  }

  /// A query listener: "[KIND id {data}, ...]" per snapshot (with metadata: and " pending",
  /// " cache"), or "error <code>".
  private func listen(_ q: Query, metadata: Bool = false) -> Recorder {
    let r = Recorder()
    recorders.append(r)
    r.registration = q.addSnapshotListener(includeMetadataChanges: metadata) { snap, error in
      if let error {
        r.add("error \(FirestoreE2ETests.describe(error))")
        return
      }
      guard let snap else { return }
      r.query(snap, metadata: metadata)
    }
    return r
  }

  /// FirestoreErrorCode's cases by raw value.
  static let codeNames = ["ok", "cancelled", "unknown", "invalidArgument", "deadlineExceeded", "notFound",
                          "alreadyExists", "permissionDenied", "resourceExhausted", "failedPrecondition",
                          "aborted", "outOfRange", "unimplemented", "internal", "unavailable", "dataLoss",
                          "unauthenticated"]

  /// The name of a Firestore error's code, or its domain and code for another error.
  static func describe(_ error: Error) -> String {
    let e = error as NSError
    guard e.domain == FirestoreErrorDomain, codeNames.indices.contains(e.code) else { return "\(e.domain) \(e.code)" }
    return codeNames[e.code]
  }

  /// The error an operation fails with (describe), or nil when it succeeds.
  private func failure(_ op: () async throws -> Void) async -> String? {
    do {
      try await op()
      return nil
    } catch {
      return Self.describe(error)
    }
  }

  /// A value as one line: keys sorted, a Timestamp as @milliseconds, NSNull as null.
  static func render(_ data: [String: Any]) -> String {
    "{" + data.keys.sorted().map { "\($0)=\(value(data[$0] ?? NSNull()))" }.joined(separator: ", ") + "}"
  }

  static func value(_ v: Any) -> String {
    switch v {
    case let t as Timestamp:
      return "@\(t.seconds * 1000 + Int64(t.nanoseconds / 1_000_000))"
    case is NSNull:
      return "null"
    case let n as NSNumber:
      if CFGetTypeID(n) == CFBooleanGetTypeID() {
        return n.boolValue ? "true" : "false"
      }
      return n.stringValue
    case let s as String:
      return s
    case let a as [Any]:
      return "[" + a.map { value($0) }.joined(separator: ", ") + "]"
    case let m as [String: Any]:
      return render(m)
    default:
      return "\(v)"
    }
  }

  /// Whether the whole of s matches the regular expression.
  static func matches(_ s: String, _ pattern: String) -> Bool {
    s.range(of: "^(?:\(pattern))$", options: .regularExpression) != nil
  }

  /// The milliseconds of the first "at=@..." in a rendered value.
  static func millis(_ s: String) -> Double? {
    guard let r = s.range(of: #"at=@\d+"#, options: .regularExpression) else { return nil }
    return Double(s[r].dropFirst(4))
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
      try await Task.sleep(nanoseconds: 50_000_000)
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

  /// Counts the runs of a transaction's block: one per attempt.
  final class Counter {
    private let lock = NSLock()
    private var n = 0
    var value: Int { lock.lock(); defer { lock.unlock() }; return n }
    func add() { lock.lock(); n += 1; lock.unlock() }
  }

  /// Moves 1 from accounts/a to accounts/b in a transaction: "committed", or "failed <code>".
  private static func move(_ db: Firestore, _ base: String, _ attempts: Counter) async -> String {
    let a = db.document("\(base)/accounts/a")
    let b = db.document("\(base)/accounts/b")
    do {
      _ = try await db.runTransaction { transaction, errorPointer -> Any? in
        attempts.add()
        do {
          let va = (try transaction.getDocument(a).get("v") as? NSNumber)?.intValue ?? 0
          let vb = (try transaction.getDocument(b).get("v") as? NSNumber)?.intValue ?? 0
          transaction.updateData(["v": va - 1], forDocument: a)
          transaction.updateData(["v": vb + 1], forDocument: b)
        } catch let error as NSError {
          errorPointer?.pointee = error
        }
        return nil
      }
      return "committed"
    } catch {
      return "failed \(describe(error))"
    }
  }

  // The second party: the REST API with an admin credential (the CLI token, which bypasses the
  // rules as a service account does), the firebaserules API, the admin Auth API and the
  // server's switch.

  private func send(_ method: String, _ url: String, _ body: Data? = nil) async throws -> (status: Int, body: Data) {
    var req = URLRequest(url: try XCTUnwrap(URL(string: url)))
    req.httpMethod = method
    req.setValue("Bearer \(Self.env["ARUMA_CLI_TOKEN"] ?? "")", forHTTPHeaderField: "Authorization")
    if let body {
      req.httpBody = body
      req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    }
    let (data, response) = try await URLSession.shared.data(for: req)
    return (try XCTUnwrap(response as? HTTPURLResponse).statusCode, data)
  }

  private func json(_ data: Data) -> [String: Any]? {
    try? JSONSerialization.jsonObject(with: data) as? [String: Any]
  }

  /// A document as the REST API returns it to the admin (nil when it does not exist).
  private func document(_ path: String) async throws -> [String: Any]? {
    let r = try await send("GET", "\(arumaURL)/v1/projects/\(projectID)/databases/(default)/documents/\(path)")
    if r.status == 404 {
      return nil
    }
    XCTAssertEqual(r.status, 200, String(decoding: r.body, as: UTF8.self))
    return json(r.body)
  }

  /// fields.<name>.<kind> of a REST document, e.g. ("count", "integerValue").
  static func field(_ doc: [String: Any]?, _ name: String, _ kind: String) -> String? {
    ((doc?["fields"] as? [String: Any])?[name] as? [String: Any])?[kind] as? String
  }

  /// Creates a ruleset with one firestore.rules file; returns its name.
  private func createRuleset(_ source: String) async throws -> String {
    let body = try JSONSerialization.data(withJSONObject: ["source": ["files": [["name": "firestore.rules", "content": source]]]])
    let r = try await send("POST", "\(arumaURL)/v1/projects/\(projectID)/rulesets", body)
    XCTAssertEqual(r.status, 200, String(decoding: r.body, as: UTF8.self))
    return try XCTUnwrap(json(r.body)?["name"] as? String)
  }

  /// Points the cloud.firestore release at a ruleset, as a deploy does.
  private func publish(_ ruleset: String) async throws {
    let release = ["name": "projects/\(projectID)/releases/cloud.firestore", "rulesetName": ruleset]
    let body = try JSONSerialization.data(withJSONObject: ["release": release])
    let r = try await send("PATCH", "\(arumaURL)/v1/projects/\(projectID)/releases/cloud.firestore", body)
    XCTAssertEqual(r.status, 200, String(decoding: r.body, as: UTF8.self))
  }

  /// Disables a user as firebase-admin's updateUser(uid, disabled) does: accounts:update.
  private func disableUser(_ uid: String) async throws {
    let body = Data(#"{"localId":"\#(uid)","disableUser":true}"#.utf8)
    let r = try await send("POST", "\(arumaURL)/identitytoolkit.googleapis.com/v1/projects/\(projectID)/accounts:update", body)
    XCTAssertEqual(r.status, 200, String(decoding: r.body, as: UTF8.self))
  }

  /// Takes the whole server down (every connection closed, new ones refused) or up again.
  private func server(_ state: String) async throws {
    var req = URLRequest(url: try XCTUnwrap(URL(string: "\(Self.env["ARUMA_SWITCH_URL"] ?? "")/\(state)")))
    req.httpMethod = "POST"
    let (_, response) = try await URLSession.shared.data(for: req)
    XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
  }
}
