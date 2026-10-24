// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import XCTest
@testable import ArumaRedirect

final class ArumaUrlsTests: XCTestCase {
  override func tearDown() {
    ArumaRedirect.reset()
    super.tearDown()
  }

  func testStorageRewrite() throws {
    let base = try ArumaBase("https://aruma.example.com", allowInsecure: false)
    for (google, aruma) in [
      ("https://firebasestorage.googleapis.com/v0/b/p.appspot.com/o?prefix=a%2F",
       "https://aruma.example.com/firebasestorage.googleapis.com/v0/b/p.appspot.com/o?prefix=a%2F"),
      ("https://firebasestorage.googleapis.com:443/v0/b/p.appspot.com/o/a%2Fb?uploadType=resumable&name=a%2Fb",
       "https://aruma.example.com/firebasestorage.googleapis.com/v0/b/p.appspot.com/o/a%2Fb?uploadType=resumable&name=a%2Fb"),
    ] {
      XCTAssertEqual(AuthEndpoints.rewrite(URL(string: google)!, to: base)?.absoluteString, aruma)
      XCTAssertTrue(AuthEndpoints.isStorage(URL(string: google)!))
    }
    for other in ["http://firebasestorage.googleapis.com/v0/b/p/o",
                  "https://firebasestorage.googleapis.com:8443/v0/b/p/o",
                  "https://firebasestorage.googleapis.com/v0/../../console"] {
      XCTAssertFalse(AuthEndpoints.matches(URL(string: other)!), other)
    }
    XCTAssertFalse(AuthEndpoints.isStorage(URL(string: "https://securetoken.googleapis.com/v1/token")!))
  }

  /// The name is decoded and written raw: FirebaseStorage reads a gs:// name as it is.
  func testFromArumaUrlWritesTheNameRaw() throws {
    let cases: [(String, String)] = [
      ("https://aruma.example.com/v0/b/p.appspot.com/o/e2e%2Fa%20b%2B%C3%A9.txt?alt=media&token=T",
       "gs://p.appspot.com/e2e/a b+é.txt"),
      ("http://localhost:9440/v0/b/p.appspot.com/o/%D8%B5%D9%88%D8%B1%D8%A9%20%D9%A1.png?alt=media&token=T",
       "gs://p.appspot.com/صورة ١.png"),
      // Google's, as the official SDK returns it (with :443), and with '+' that is not a space.
      ("https://firebasestorage.googleapis.com:443/v0/b/p.appspot.com/o/a+b%25c?alt=media",
       "gs://p.appspot.com/a+b%c"),
      ("gs://p.appspot.com/a b.txt", "gs://p.appspot.com/a b.txt"),
    ]
    for (link, gs) in cases {
      XCTAssertEqual(try ArumaUrls.fromArumaUrl(link), gs, link)
      XCTAssertEqual(try ArumaUrls.fromArumaUrl(link, bucket: "p.appspot.com"), gs, link)
    }
  }

  /// Everything reference(forURL:) would end the app on is thrown instead.
  func testFromArumaUrlRefuses() {
    for bad in [
      "https://aruma.example.com/firebasestorage.googleapis.com/v0/b/p/o/x",  // the prefixed form
      "https://aruma.example.com/v0/b/p/o/", "https://aruma.example.com/v0/b//o/x",
      "https://aruma.example.com/v0/b/p", "https://aruma.example.com/v1/b/p/o/x",
      "https://aruma.example.com/v0/b/p/o/%FF",
      "https://aruma.example.com/v0/b/a%2Fb/o/x", "ftp://aruma.example.com/v0/b/p/o/x",
      "gs://p", "gs://p/", "gs:///x", "not a url", "",
    ] {
      XCTAssertThrowsError(try ArumaUrls.fromArumaUrl(bad), bad) {
        guard case .notStorageURL? = $0 as? ArumaUrlError else { return XCTFail("\(bad): \($0)") }
      }
    }
    XCTAssertThrowsError(try ArumaUrls.fromArumaUrl("https://a.example/v0/b/other/o/x", bucket: "p")) {
      XCTAssertEqual($0 as? ArumaUrlError, .bucketMismatch(expected: "p", found: "other"))
    }
  }

  func testToArumaUrl() throws {
    let google = "https://firebasestorage.googleapis.com:443/v0/b/p.appspot.com/o/a%2Fb?alt=media&token=T"
    XCTAssertThrowsError(try ArumaUrls.toArumaUrl(google))
    try ArumaRedirect.install(urlString: "http://LOCALHOST:9440/")
    XCTAssertEqual(try ArumaUrls.toArumaUrl(google), "http://localhost:9440/v0/b/p.appspot.com/o/a%2Fb?alt=media&token=T")
    XCTAssertEqual(try ArumaUrls.toArumaUrl("https://FirebaseStorage.googleapis.com./v0/b/p/o/x"),
                   "http://localhost:9440/v0/b/p/o/x")
    for unchanged in ["http://localhost:9440/v0/b/p/o/x?alt=media", "https://example.com/v0/b/p/o/x",
                      "https://firebasestorage.googleapis.com:8443/v0/b/p/o/x"] {
      XCTAssertEqual(try ArumaUrls.toArumaUrl(unchanged), unchanged)
    }
    try ArumaRedirect.install(urlString: "https://example.com/aruma")
    XCTAssertThrowsError(try ArumaUrls.toArumaUrl(google)) {
      guard case .notConfigured? = $0 as? ArumaUrlError else { return XCTFail("\($0)") }
    }
  }
}
