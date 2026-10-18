// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import XCTest
@testable import ArumaRedirect

final class RewriteTests: XCTestCase {
  private func base(_ s: String, insecure: Bool = false) throws -> ArumaBase {
    try ArumaBase(s, allowInsecure: insecure)
  }

  private func rewrite(_ s: String, _ b: String = "https://aruma.example.com") throws -> String? {
    AuthEndpoints.rewrite(URL(string: s)!, to: try base(b))?.absoluteString
  }

  func testTheThreeFamiliesKeepPathAndQuery() throws {
    XCTAssertEqual(
      try rewrite("https://identitytoolkit.googleapis.com/v2/accounts:revokeToken?key=K"),
      "https://aruma.example.com/identitytoolkit.googleapis.com/v2/accounts:revokeToken?key=K")
    XCTAssertEqual(
      try rewrite("https://securetoken.googleapis.com/v1/token?key=K"),
      "https://aruma.example.com/securetoken.googleapis.com/v1/token?key=K")
    XCTAssertEqual(
      try rewrite("https://www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K"),
      "https://aruma.example.com/www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=K")
  }

  func testBasePortAndPrefix() throws {
    XCTAssertEqual(
      try rewrite("https://securetoken.googleapis.com/v1/token?key=K", "http://localhost:8092/"),
      "http://localhost:8092/securetoken.googleapis.com/v1/token?key=K")
    XCTAssertEqual(
      try rewrite("https://securetoken.googleapis.com/v1/token", "https://example.com/aruma//"),
      "https://example.com/aruma/securetoken.googleapis.com/v1/token")
  }

  func testPercentEncodingIsPreserved() throws {
    XCTAssertEqual(
      try rewrite("https://identitytoolkit.googleapis.com/v1/projects?key=a%2Bb&x=%20"),
      "https://aruma.example.com/identitytoolkit.googleapis.com/v1/projects?key=a%2Bb&x=%20")
  }

  func testOtherRequestsAreNotRewritten() throws {
    for url in [
      "https://firebaseinstallations.googleapis.com/v1/projects/p/installations",
      "https://www.googleapis.com/oauth2/v4/token",
      "https://www.googleapis.com/identitytoolkit/v3/other",
      "http://identitytoolkit.googleapis.com/v1/accounts:signUp",
      "https://securetoken.googleapis.com:8443/v1/token",
      "https://identitytoolkit.googleapis.com.evil.example/v1/accounts:signUp",
      "https://securetoken.googleapis.com/v1/../../console",
      "https://example.com/securetoken.googleapis.com/v1/token",
    ] {
      XCTAssertNil(try rewrite(url), url)
      XCTAssertFalse(AuthEndpoints.matches(URL(string: url)!), url)
    }
  }

  func testHostMatchIsCaseInsensitive() throws {
    XCTAssertEqual(
      try rewrite("HTTPS://SecureToken.GoogleAPIs.com/v1/token"),
      "https://aruma.example.com/securetoken.googleapis.com/v1/token")
  }

  func testBaseValidation() throws {
    XCTAssertNoThrow(try base("https://aruma.example.com"))
    XCTAssertNoThrow(try base("http://localhost:8092"))
    XCTAssertNoThrow(try base("http://127.0.0.1:8092"))
    XCTAssertNoThrow(try base("http://[::1]:8092"))
    XCTAssertNoThrow(try base("http://aruma.lan:8092", insecure: true))
    XCTAssertThrowsError(try base("http://aruma.example.com")) {
      XCTAssertEqual($0 as? ArumaRedirectError, .insecureURL("http://aruma.example.com"))
    }
    XCTAssertThrowsError(try base("http://127.0.0.1.evil.example"))
    XCTAssertThrowsError(try base("http://localhost.evil.example"))
    for bad in ["", "aruma.example.com", "ftp://aruma.example.com", "https://", "https://a.example?x=1",
                "https://a.example#f", "https://user:pw@a.example"] {
      XCTAssertThrowsError(try base(bad), bad) {
        guard case .invalidURL? = $0 as? ArumaRedirectError else { return XCTFail("\(bad): \($0)") }
      }
    }
  }

  func testLoggableDropsQuery() {
    let url = URL(string: "https://www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword?key=SECRET")!
    XCTAssertEqual(ArumaRedirectURLProtocol.loggable(url),
                   "https://www.googleapis.com/identitytoolkit/v3/relyingparty/verifyPassword")
  }
}

final class BodyTests: XCTestCase {
  func testHTTPBodyWins() throws {
    var r = URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!)
    r.httpBody = Data("a=1".utf8)
    XCTAssertEqual(try RequestBody.read(from: r), Data("a=1".utf8))
  }

  func testStreamIsReadFully() throws {
    let payload = Data((0..<300_000).map { UInt8($0 % 251) })
    var r = URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!)
    r.httpBodyStream = InputStream(data: payload)
    XCTAssertEqual(try RequestBody.read(from: r), payload)
  }

  func testEmptyStreamAndNoBody() throws {
    var r = URLRequest(url: URL(string: "https://securetoken.googleapis.com/v1/token")!)
    XCTAssertNil(try RequestBody.read(from: r))
    r.httpBodyStream = InputStream(data: Data())
    XCTAssertEqual(try RequestBody.read(from: r), Data())
  }

  func testLimit() {
    XCTAssertThrowsError(try RequestBody.read(InputStream(data: Data(count: 100)), limit: 10))
  }
}
