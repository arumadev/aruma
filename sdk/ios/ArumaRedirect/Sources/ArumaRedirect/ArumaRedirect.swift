// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import Foundation

/// Sends the official Firebase Auth SDK to an Aruma server by rewriting its requests inside the app.
///
/// The SDK keeps its production mode (no `useEmulator`). Only these requests are rewritten, and only the
/// host changes, because Aruma serves Google's paths under its own host (ADR-014):
///
/// - `https://identitytoolkit.googleapis.com/<path>` → `<ArumaURL>/identitytoolkit.googleapis.com/<path>`
/// - `https://securetoken.googleapis.com/<path>` → `<ArumaURL>/securetoken.googleapis.com/<path>`
/// - `https://www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` →
///   `<ArumaURL>/www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>`
///
/// - `https://firebasestorage.googleapis.com/v0/<path>` →
///   `<ArumaURL>/firebasestorage.googleapis.com/v0/<path>` (Cloud Storage, the fallback below)
///
/// Every other request is passed through untouched.
///
/// Cloud Storage's production path is the patched FirebaseStorage from the SwiftPM mirror
/// (sdk/ios/firebase-storage-patch): it reads the same `ArumaURL`, talks to Aruma directly and
/// returns download URLs on Aruma. The Storage rewrite here catches an official FirebaseStorage,
/// whose download URLs still name Google (see ``ArumaUrls``); it logs that once. With
/// `ArumaStorageRedirect` false, such a request is refused instead.
///
/// Normally nothing is called: the package's loader reads `ArumaURL` from Info.plist at launch.
/// Apps that prefer code call ``install(url:allowInsecure:)`` before Firebase sends its first request.
public enum ArumaRedirect {
  /// Info.plist keys read at launch.
  public enum InfoKey {
    /// String: the Aruma server, e.g. `https://aruma.example.com`. Absent or empty: the package does nothing.
    public static let url = "ArumaURL"
    /// Bool: accept `http` to a host that is not loopback. Development only.
    public static let allowInsecure = "ArumaAllowInsecureURL"
    /// Bool: log each rewrite (method, host and path; never the query, headers or body).
    public static let log = "ArumaRedirectLog"
    /// Bool, default true: rewrite Cloud Storage requests to `firebasestorage.googleapis.com` too,
    /// as a fallback for the official FirebaseStorage. Set it to false in an app built with the
    /// patched FirebaseStorage (sdk/ios/firebase-storage-patch) to make the patch mandatory: a
    /// Storage request to Google is then refused instead of rewritten.
    public static let storageRedirect = "ArumaStorageRedirect"
  }

  enum Mode: Equatable {
    case forward(ArumaBase)
    /// ArumaURL was present but refused. Auth requests fail instead of reaching Google.
    case blocked(String)
  }

  private static let lock = NSLock()
  private static var _mode: Mode?
  private static var _logRequests = false
  private static var _loaderRan = false

  static var mode: Mode? {
    lock.lock(); defer { lock.unlock() }
    return _mode
  }

  static var logRequests: Bool {
    lock.lock(); defer { lock.unlock() }
    return _logRequests
  }

  /// The Aruma server Auth requests are sent to, or nil when the redirect is not active.
  public static var baseURL: URL? {
    if case .forward(let base)? = mode { return base.url }
    return nil
  }

  /// True once the launch loader has run (whether or not ArumaURL was set).
  public static var loaderRan: Bool {
    lock.lock(); defer { lock.unlock() }
    return _loaderRan
  }

  /// Rewrites Firebase Auth requests to `url`. Call it before Firebase sends its first Auth request,
  /// for example first thing in `application(_:didFinishLaunchingWithOptions:)`.
  ///
  /// - Parameters:
  ///   - url: the Aruma server (`https://…`; `http://` only for a loopback host).
  ///   - allowInsecure: accept `http` to any host. Development only.
  /// - Throws: ``ArumaRedirectError`` when the URL is refused. Nothing is installed then.
  public static func install(url: URL, allowInsecure: Bool = false) throws {
    try install(urlString: url.absoluteString, allowInsecure: allowInsecure)
  }

  /// String form of ``install(url:allowInsecure:)``.
  ///
  /// It also checks which FirebaseStorage the app links (``firebaseStorageBuild``). The official
  /// build is logged loudly; with ``storageRedirect`` false it is refused: every Auth and Storage
  /// request then fails, and this throws ``ArumaRedirectError/officialFirebaseStorage(_:)``.
  public static func install(urlString: String, allowInsecure: Bool = false) throws {
    let base = try ArumaBase(urlString, allowInsecure: allowInsecure)
    let build = StorageBuildCheck.build(lookup: classLookup)
    lock.lock(); _storageBuild = build; lock.unlock()
    let (lines, refusal) = StorageBuildCheck.verdict(build, storageRedirect: AuthEndpoints.storageRedirect)
    lines.forEach(log)
    if let refusal {
      set(.blocked(refusal))
      throw ArumaRedirectError.officialFirebaseStorage(refusal)
    }
    set(.forward(base))
    log("installed: Firebase Auth requests go to \(base.url.absoluteString)"
      + (AuthEndpoints.storageRedirect ? ", and so do requests to \(AuthEndpoints.storageHost)" : ""))
  }

  /// The FirebaseStorage install found in the process, or nil before install.
  public static var firebaseStorageBuild: FirebaseStorageBuild? {
    lock.lock(); defer { lock.unlock() }
    return _storageBuild
  }

  private static var _storageBuild: FirebaseStorageBuild?
  /// Test hooks: how classes are looked up, and how the launch refusal stops the app.
  static var classLookup: (String) -> AnyClass? = { NSClassFromString($0) }
  static var stop: (String) -> Void = { fatalError("ArumaRedirect: \($0)") }

  /// Reads ``InfoKey`` values from `bundle` and installs the redirect. Called at launch by the loader.
  ///
  /// When ArumaURL is absent or empty, nothing happens. When it is present but refused, Auth requests
  /// are made to fail (and logged once) rather than being sent to Google with this app's credentials.
  public static func installFromInfoPlist(_ bundle: Bundle = .main) {
    guard let raw = bundle.object(forInfoDictionaryKey: InfoKey.url) as? String,
          !raw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    else { return }
    let allowInsecure = boolValue(bundle.object(forInfoDictionaryKey: InfoKey.allowInsecure))
    if let storage = bundle.object(forInfoDictionaryKey: InfoKey.storageRedirect) {
      AuthEndpoints.storageRedirect = boolValue(storage)
    }
    lock.lock()
    _logRequests = boolValue(bundle.object(forInfoDictionaryKey: InfoKey.log))
    lock.unlock()
    do {
      try install(urlString: raw, allowInsecure: allowInsecure)
    } catch ArumaRedirectError.officialFirebaseStorage(let reason) {
      // The documented fail-closed mode: the app stops at launch, as on Android.
      stop(reason)
    } catch {
      set(.blocked(String(describing: error)))
      log("refused: \(error). Firebase Auth requests will fail instead of reaching Google")
    }
  }

  static func markLoaderRan() {
    lock.lock(); _loaderRan = true; lock.unlock()
  }

  /// Whether Cloud Storage requests to Google are rewritten (true, the default) or refused.
  /// See ``InfoKey/storageRedirect``. Read per request.
  public static var storageRedirect: Bool {
    get { AuthEndpoints.storageRedirect }
    set { AuthEndpoints.storageRedirect = newValue }
  }

  /// The S0-B spike's switch, kept for test/experiments/storage-path. The Storage redirect is now
  /// on by default, and the patched FirebaseStorage is the production path.
  @available(*, deprecated, message: "use storageRedirect or Info.plist ArumaStorageRedirect")
  public static func setStorageSpike(_ on: Bool) {
    AuthEndpoints.storageRedirect = on
  }

  /// Test hook: forget the installed mode. The URLProtocol stays registered and passes everything through.
  static func reset() {
    lock.lock(); _mode = nil; _logRequests = false; _storageBuild = nil; lock.unlock()
    AuthEndpoints.resetStorage()
    classLookup = { NSClassFromString($0) }
    stop = { fatalError("ArumaRedirect: \($0)") }
  }

  static func setLogRequests(_ on: Bool) {
    lock.lock(); _logRequests = on; lock.unlock()
  }

  private static func set(_ mode: Mode) {
    lock.lock(); _mode = mode; lock.unlock()
    SessionHook.installOnce()
  }

  static func log(_ message: String) {
    NSLog("ARUMA_REDIRECT %@", message)
  }

  private static func boolValue(_ value: Any?) -> Bool {
    switch value {
    case let b as Bool: return b
    case let n as NSNumber: return n.boolValue
    case let s as String: return ["1", "yes", "true"].contains(s.lowercased())
    default: return false
    }
  }
}

/// Entry point for the Objective-C loader (`+load`), which cannot import this module directly.
@objc(ArumaRedirectAutoInstaller)
public final class ArumaRedirectAutoInstaller: NSObject {
  @objc public static func installFromMainBundle() {
    ArumaRedirect.markLoaderRan()
    ArumaRedirect.installFromInfoPlist(.main)
  }
}
