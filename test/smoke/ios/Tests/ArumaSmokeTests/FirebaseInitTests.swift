import ExceptionCatcher
import FirebaseCore
import FirebaseInstallations
import XCTest

final class FirebaseInitTests: XCTestCase {
    override class func setUp() {
        super.setUp()
        NetworkGuard.install()
    }

    private func resource(_ name: String) throws -> URL {
        try XCTUnwrap(Bundle.module.url(forResource: "Fixtures/\(name)", withExtension: nil), "missing \(name)")
    }

    private func expected() throws -> [String: Any] {
        let data = try Data(contentsOf: resource("expected.json"))
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    /// Reads the plist, configures the default app (FirebaseCore validates GOOGLE_APP_ID and
    /// raises on a bad one) and instantiates Installations (which validates the API key and
    /// raises on a bad one). A raised exception fails the test instead of crashing it.
    func testConfigureWithArumaPlist() throws {
        let path = try resource("GoogleService-Info.plist").path
        let options = try XCTUnwrap(FirebaseOptions(contentsOfFile: path))
        let exp = try expected()
        let ios = try XCTUnwrap(exp["ios"] as? [String: String])
        XCTAssertEqual(options.googleAppID, ios["appId"])
        XCTAssertEqual(options.apiKey, ios["apiKey"])
        XCTAssertEqual(options.projectID, exp["projectId"] as? String)
        XCTAssertEqual(options.gcmSenderID, exp["projectNumber"] as? String)
        XCTAssertEqual(options.storageBucket, exp["storageBucket"] as? String)
        XCTAssertEqual(options.databaseURL, exp["databaseURL"] as? String)
        XCTAssertEqual(options.bundleID, ios["namespace"])

        let before = NetworkGuard.attempts.count
        let raised = ArumaCatchException {
            FirebaseApp.configure(options: options)
            _ = Installations.installations()
        }
        XCTAssertNil(raised, "Firebase rejected the config: \(raised?.reason ?? "")")
        XCTAssertNotNil(FirebaseApp.app())

        // Give eager components a moment to show any network activity.
        RunLoop.current.run(until: Date().addingTimeInterval(2))
        XCTAssertEqual(Array(NetworkGuard.attempts.dropFirst(before)), [], "network access attempted")
    }

    /// The guard must see requests from sessions with their own configuration, as Firebase's.
    func testNetworkGuardSeesPrivateSessions() {
        let before = NetworkGuard.attempts.count
        let done = expectation(description: "request refused")
        URLSession(configuration: .ephemeral).dataTask(with: URL(string: "https://example.com/")!) { _, _, error in
            XCTAssertNotNil(error)
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        XCTAssertEqual(Array(NetworkGuard.attempts.dropFirst(before)), ["https://example.com/"])
    }

    /// The negative control: a key of the wrong length is refused by Installations, proving the
    /// check runs and that Aruma's keys pass it for the right reason.
    func testWrongLengthKeyIsRefused() throws {
        let path = try resource("GoogleService-Info.plist").path
        let options = try XCTUnwrap(FirebaseOptions(contentsOfFile: path))
        options.apiKey = String(options.apiKey!.dropLast())
        let raised = ArumaCatchException {
            FirebaseApp.configure(name: "badkey", options: options)
            _ = Installations.installations(app: FirebaseApp.app(name: "badkey")!)
        }
        let exception = try XCTUnwrap(raised, "a 38-character key was accepted")
        XCTAssertTrue((exception.reason ?? "").localizedCaseInsensitiveContains("API key"), exception.reason ?? "")
    }
}
