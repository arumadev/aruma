import FirebaseCore
import FirebaseFirestore
import SpikeSupport
import XCTest

/// The official FirebaseFirestore against the transport spike server, set up in code with
/// FirestoreSettings.host and isSSLEnabled. run.sh runs it once per listener and passes
/// TEST_RUNNER_SPIKE_ADDR, _SPIKE_SSL, _SPIKE_LABEL, _SPIKE_HOLD (seconds the Listen stream must
/// have been open when the later message arrives) and _SPIKE_LISTEN_FOR.
///
/// - test1Unary: a transaction, so BatchGetDocuments and Commit (mobile's only unary call);
/// - test2Write: DocumentReference.setData, so the Write stream's handshake and one write;
/// - test3Listen: a snapshot listener held until the server's later document (65 s in).
final class TransportSpikeTests: XCTestCase {
  private static let env = ProcessInfo.processInfo.environment
  private static var label: String { env["SPIKE_LABEL"] ?? "spike" }
  private static var db: Firestore!

  override class func setUp() {
    super.setUp()
    guard db == nil else { return }
    // Made-up identifiers: the SDK checks their form, not their values, and nothing reaches Google.
    let options = FirebaseOptions(googleAppID: "1:1234567890:ios:0123456789abcdef", gcmSenderID: "1234567890")
    options.projectID = "spike-project"
    options.apiKey = "AIzaSyD-spike-made-up-key-0000000000000"
    let name = label.replacingOccurrences(of: "-", with: "")
    FirebaseApp.configure(name: name, options: options)
    let firestore = Firestore.firestore(app: FirebaseApp.app(name: name)!)
    let settings = FirestoreSettings()
    settings.host = env["SPIKE_ADDR"] ?? "127.0.0.1:9100"
    settings.isSSLEnabled = env["SPIKE_SSL"] == "true"
    settings.cacheSettings = MemoryCacheSettings()
    firestore.settings = settings
    db = firestore
    print("label=\(label) host=\(settings.host) ssl=\(settings.isSSLEnabled)")
  }

  private var label: String { Self.label }
  private var db: Firestore { Self.db }

  func test1Unary() {
    let doc = db.document("spike/tx")
    let done = expectation(description: "transaction")
    var failure: Error?
    db.runTransaction({ tx, errorPointer in
      do {
        _ = try tx.getDocument(doc)
      } catch let e as NSError {
        errorPointer?.pointee = e
        return nil
      }
      tx.setData(["n": 1], forDocument: doc)
      return nil
    }) { _, error in
      failure = error
      done.fulfill()
    }
    let waited = XCTWaiter.wait(for: [done], timeout: 60)
    let outcome = waited != .completed ? "FAIL timed out" : failure.map { "FAIL \($0)" } ?? "PASS"
    spikeResult(label, "unary", outcome)
    XCTAssertEqual(outcome, "PASS")
  }

  func test2Write() {
    let done = expectation(description: "set")
    var failure: Error?
    db.document("spike/write").setData(["n": 1]) { error in
      failure = error
      done.fulfill()
    }
    let waited = XCTWaiter.wait(for: [done], timeout: 60)
    let outcome = waited != .completed ? "FAIL timed out" : failure.map { "FAIL \($0)" } ?? "PASS"
    spikeResult(label, "write", outcome)
    XCTAssertEqual(outcome, "PASS")
  }

  func test3Listen() {
    let hold = TimeInterval(Self.env["SPIKE_HOLD"] ?? "60") ?? 60
    let listenFor = TimeInterval(Self.env["SPIKE_LISTEN_FOR"] ?? "100") ?? 100
    let later = expectation(description: "later snapshot")
    let begin = Date()
    var firstFromServer = false
    var problem: Error?
    let reg = db.document("spike/doc").addSnapshotListener(includeMetadataChanges: true) { snap, error in
      if let error {
        problem = error
        later.fulfill()
        return
      }
      guard let snap else { return }
      let fromCache = snap.metadata.isFromCache
      print("snapshot exists=\(snap.exists) fromCache=\(fromCache) after=\(Int(Date().timeIntervalSince(begin)))s")
      if !fromCache && !snap.exists { firstFromServer = true }
      if !fromCache && snap.exists && snap.get("later") as? Bool == true { later.fulfill() }
    }
    let waited = XCTWaiter.wait(for: [later], timeout: listenFor)
    reg.remove()
    let held = Int(Date().timeIntervalSince(begin))
    let outcome: String
    if waited != .completed {
      outcome = "FAIL no later snapshot after \(held)s"
    } else if let problem {
      outcome = "FAIL \(problem)"
    } else if !firstFromServer {
      outcome = "FAIL no first snapshot from the server"
    } else if Double(held) < hold {
      outcome = "FAIL later snapshot after \(held)s, before \(Int(hold))s"
    } else {
      outcome = "PASS held \(held)s"
    }
    spikeResult(label, "stream", outcome)
    XCTAssertTrue(outcome.hasPrefix("PASS"), outcome)
  }
}
