import ArumaRedirect
import FirebaseAuth
import FirebaseCore
import XCTest

/// The official FirebaseAuth for iOS in production mode (no useEmulator), configured only with the
/// GoogleService-Info.plist Aruma generated and sent to Aruma by ArumaRedirect (ArumaURL in the
/// host app's Info.plist), against a real server, with every other request refused (LoopbackGuard).
/// The scenarios follow test/e2e/web-auth and docs/reference/compatibility.md.
///
/// run.sh passes the server through TEST_RUNNER_ARUMA_URL, TEST_RUNNER_ARUMA_PROJECT_ID and
/// TEST_RUNNER_ARUMA_CLI_TOKEN (xcodebuild hands TEST_RUNNER_* to the tests without the prefix).
final class AuthE2ETests: XCTestCase {
  private static let env = ProcessInfo.processInfo.environment
  private var arumaURL: String { Self.env["ARUMA_URL"] ?? "" }
  private var projectID: String { Self.env["ARUMA_PROJECT_ID"] ?? "" }
  /// Tells this run's addresses apart from an earlier run's on the same server.
  private static let run = String(UInt64(Date().timeIntervalSince1970 * 1000), radix: 36)

  private var auth: Auth { Auth.auth() }
  private var blockedBefore = 0

  override class func setUp() {
    super.setUp()
    LoopbackGuard.install()
    if FirebaseApp.app() == nil {
      FirebaseApp.configure()  // GoogleService-Info.plist from the host app's bundle
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
      var description: String { "run through test/e2e/ios-auth/run.sh (ARUMA_URL is not set)" }
    }
    guard !arumaURL.isEmpty else { throw NotConfigured() }
    blockedBefore = LoopbackGuard.blocked.count
    try auth.signOut()
  }

  override func tearDown() {
    // The guard: nothing but this machine was asked for during the test.
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)), [], "requests leaving the machine")
    super.tearDown()
  }

  private func email(_ name: String) -> String { "\(name)-ios-\(Self.run)@example.com" }

  /// The AuthErrorCode a call fails with, or nil when it succeeds.
  private func authError(_ body: () async throws -> Void) async -> AuthErrorCode? {
    do {
      try await body()
      return nil
    } catch {
      let ns = error as NSError
      XCTAssertEqual(ns.domain, AuthErrorDomain, "not an Auth error: \(error)")
      return AuthErrorCode(rawValue: ns.code)
    }
  }

  func testProductionModeThroughArumaRedirect() throws {
    // The redirect read ArumaURL from the host app's Info.plist at launch.
    XCTAssertTrue(ArumaRedirect.loaderRan)
    XCTAssertEqual(ArumaRedirect.baseURL?.absoluteString, arumaURL)
    let options = try XCTUnwrap(FirebaseApp.app()?.options)
    XCTAssertEqual(options.projectID, projectID)
    XCTAssertEqual(options.bundleID, Bundle.main.bundleIdentifier)
    // Only FirebaseAuth and FirebaseCore are linked: Installations and Analytics would call Google
    // at launch whatever ArumaRedirect does.
    for name in ["FIRInstallations", "FIRAnalytics", "FIRMessaging"] {
      XCTAssertNil(NSClassFromString(name), "\(name) is linked in")
    }
  }

  /// The guard refuses what ArumaRedirect does not take, in sessions with their own configuration
  /// as Firebase's: here the request Installations would send at launch. Nothing leaves the machine.
  func testGuardRefusesOtherGoogleRequests() async throws {
    let url = try XCTUnwrap(URL(string: "https://firebaseinstallations.googleapis.com/v1/projects/x/installations"))
    do {
      _ = try await URLSession(configuration: .ephemeral).data(from: url)
      XCTFail("the request was not refused")
    } catch {
      XCTAssertEqual((error as? URLError)?.code, .notConnectedToInternet)
    }
    XCTAssertEqual(Array(LoopbackGuard.blocked.dropFirst(blockedBefore)),
                   ["https://firebaseinstallations.googleapis.com/v1/projects/x/installations"])
    LoopbackGuard.forget(LoopbackGuard.blocked.count - blockedBefore)
  }

  func testEmailPasswordLifecycle() async throws {
    let addr = email("life")
    let created = try await auth.createUser(withEmail: addr, password: "secret1")
    let uid = created.user.uid
    XCTAssertEqual(uid.count, 28)
    XCTAssertEqual(created.user.email, addr)
    // FirebaseAuth for iOS still uses the v3 relyingparty API for these calls.
    XCTAssertTrue(LoopbackGuard.redirected.contains("www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser"),
                  "\(LoopbackGuard.redirected)")

    try auth.signOut()
    XCTAssertNil(auth.currentUser)

    let signedIn = try await auth.signIn(withEmail: addr, password: "secret1")
    XCTAssertEqual(signedIn.user.uid, uid)

    // A forced refresh goes to the Secure Token endpoint and yields a token Aruma issued.
    let before = LoopbackGuard.redirected.count
    let token = try await signedIn.user.getIDTokenResult(forcingRefresh: true)
    XCTAssertTrue(LoopbackGuard.redirected.dropFirst(before).contains("securetoken.googleapis.com/v1/token"),
                  "\(LoopbackGuard.redirected)")
    XCTAssertEqual(token.claims["iss"] as? String, "\(arumaURL)/issuers/\(projectID)")
    XCTAssertEqual(token.claims["aud"] as? String, projectID)
    XCTAssertEqual(token.claims["sub"] as? String, uid)
    XCTAssertEqual(token.signInProvider, "password")

    let change = signedIn.user.createProfileChangeRequest()
    change.displayName = "سارة"
    try await change.commitChanges()
    try await signedIn.user.reload()
    XCTAssertEqual(auth.currentUser?.displayName, "سارة")

    // A password change after a recent sign-in keeps this session.
    let user = try XCTUnwrap(auth.currentUser)
    try await user.reauthenticate(with: EmailAuthProvider.credential(withEmail: addr, password: "secret1"))
    try await user.updatePassword(to: "secret2")
    _ = try await user.getIDTokenResult(forcingRefresh: true)
    try auth.signOut()
    let oldPassword = await authError { _ = try await self.auth.signIn(withEmail: addr, password: "secret1") }
    XCTAssertEqual(oldPassword, .invalidCredential)
    let again = try await auth.signIn(withEmail: addr, password: "secret2")
    XCTAssertEqual(again.user.uid, uid)

    try await again.user.delete()
    XCTAssertNil(auth.currentUser)
    let deleted = await authError { _ = try await self.auth.signIn(withEmail: addr, password: "secret2") }
    XCTAssertEqual(deleted, .invalidCredential)
  }

  func testErrorCodes() async throws {
    let addr = email("errors")
    _ = try await auth.createUser(withEmail: addr, password: "secret1")
    try auth.signOut()

    let duplicate = await authError { _ = try await self.auth.createUser(withEmail: addr, password: "secret1") }
    XCTAssertEqual(duplicate, .emailAlreadyInUse)
    let weak = await authError { _ = try await self.auth.createUser(withEmail: "weak-" + addr, password: "123") }
    XCTAssertEqual(weak, .weakPassword)
    let invalidEmail = await authError { _ = try await self.auth.createUser(withEmail: "not-an-email", password: "secret1") }
    XCTAssertEqual(invalidEmail, .invalidEmail)
    let wrong = await authError { _ = try await self.auth.signIn(withEmail: addr, password: "wrong!!") }
    XCTAssertEqual(wrong, .invalidCredential)
    let nobody = await authError { _ = try await self.auth.signIn(withEmail: self.email("nobody"), password: "secret1") }
    XCTAssertEqual(nobody, .invalidCredential)
    XCTAssertNil(auth.currentUser)

    let user = try await auth.signIn(withEmail: addr, password: "secret1").user
    try await user.delete()
  }

  func testAnonymous() async throws {
    let result = try await auth.signInAnonymously()
    XCTAssertTrue(result.user.isAnonymous)
    XCTAssertEqual(result.user.uid.count, 28)
    let token = try await result.user.getIDTokenResult(forcingRefresh: true)
    XCTAssertEqual(token.signInProvider, "anonymous")
    XCTAssertEqual(token.claims["sub"] as? String, result.user.uid)
    try await result.user.delete()
    XCTAssertNil(auth.currentUser)
  }

  func testPasswordResetThroughDevMailbox() async throws {
    let addr = email("reset")
    _ = try await auth.createUser(withEmail: addr, password: "secret1")
    try auth.signOut()
    try await auth.sendPasswordReset(withEmail: addr)
    let code = try await mailboxCode(to: addr)

    let verified = try await auth.verifyPasswordResetCode(code)
    XCTAssertEqual(verified, addr)
    let weak = await authError { try await self.auth.confirmPasswordReset(withCode: code, newPassword: "123") }
    XCTAssertEqual(weak, .weakPassword)
    try await auth.confirmPasswordReset(withCode: code, newPassword: "secret2")
    let reused = await authError { try await self.auth.confirmPasswordReset(withCode: code, newPassword: "secret3") }
    XCTAssertEqual(reused, .invalidActionCode)

    let old = await authError { _ = try await self.auth.signIn(withEmail: addr, password: "secret1") }
    XCTAssertEqual(old, .invalidCredential)
    let user = try await auth.signIn(withEmail: addr, password: "secret2").user
    XCTAssertEqual(user.email, addr)
    try await user.delete()
  }

  /// The oobCode of the newest dev mailbox message to an address, as the user would follow the link
  /// (the console API, with the CLI token run.sh passes).
  private func mailboxCode(to addr: String) async throws -> String {
    var request = URLRequest(url: try XCTUnwrap(URL(string: "\(arumaURL)/console/v1/projects/\(projectID)/auth/mailbox")))
    request.setValue("Bearer \(Self.env["ARUMA_CLI_TOKEN"] ?? "")", forHTTPHeaderField: "Authorization")
    let (data, response) = try await URLSession.shared.data(for: request)
    XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
    struct Mailbox: Decodable { struct Message: Decodable { let to: String; let link: String }; let messages: [Message] }
    let message = try XCTUnwrap(try JSONDecoder().decode(Mailbox.self, from: data).messages.first { $0.to == addr },
                                "no message to \(addr) in the dev mailbox")
    let link = try XCTUnwrap(URLComponents(string: message.link))
    return try XCTUnwrap(link.queryItems?.first { $0.name == "oobCode" }?.value)
  }
}
