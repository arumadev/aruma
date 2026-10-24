import ArumaRedirect
import Flutter
import Foundation

/// The e2e app's native half on iOS: installs the URLSession guard when the engine registers its
/// plugins (before Dart asks FirebaseStorage for anything), and answers the "aruma_e2e" channel:
/// `info` (which FirebaseStorage ArumaRedirect found linked), `guard` and `selfTest`.
public final class E2eNativePlugin: NSObject, FlutterPlugin {
  public static func register(with registrar: FlutterPluginRegistrar) {
    LoopbackGuard.install()
    let channel = FlutterMethodChannel(name: "aruma_e2e", binaryMessenger: registrar.messenger())
    registrar.addMethodCallDelegate(E2eNativePlugin(), channel: channel)
  }

  public func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
    switch call.method {
    case "info":
      result([
        "platform": "ios",
        // .patched: the mirror's FirebaseStorage (its marker class FIRArumaStorageBuild is linked).
        "storageBuild": ArumaRedirect.firebaseStorageBuild?.rawValue ?? "not installed",
        "arumaRedirect": ArumaRedirect.baseURL?.absoluteString ?? "not installed",
      ])
    case "guard":
      let blocked = LoopbackGuard.blocked
      let redirected = LoopbackGuard.redirected
      result([
        "outside": blocked,
        "redirected": redirected,
        "summary": "\(LoopbackGuard.loopbackPaths.count) URLSession requests to this machine, "
          + "\(redirected.count) taken by ArumaRedirect (\(Set(redirected.map { $0.split(separator: "/").first ?? "" }).sorted().joined(separator: ", "))), "
          + "\(blocked.count) refused",
      ])
    case "selfTest":
      // A request off this machine must be refused and recorded.
      let before = LoopbackGuard.blocked.count
      let task = URLSession(configuration: .ephemeral).dataTask(with: URL(string: "https://192.0.2.1/")!) { _, _, error in
        DispatchQueue.main.async {
          let blocked = LoopbackGuard.blocked
          guard blocked.count == before + 1, blocked.last == "https://192.0.2.1/",
                (error as? URLError)?.code == .notConnectedToInternet
          else {
            result(FlutterError(code: "selfTest", message: "the guard did not refuse https://192.0.2.1/: \(blocked), \(String(describing: error))", details: nil))
            return
          }
          LoopbackGuard.forget(1)
          result("refused and recorded https://192.0.2.1/ in an ephemeral session")
        }
      }
      task.resume()
    default:
      result(FlutterMethodNotImplemented)
    }
  }
}
