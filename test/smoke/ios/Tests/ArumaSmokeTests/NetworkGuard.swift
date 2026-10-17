import Foundation

/// Refuses and records every URL request. URLProtocol.registerClass alone misses sessions built
/// from their own configuration, as Firebase's are, so the default and ephemeral configuration
/// factories are swizzled to put the guard first in every session.
final class NetworkGuard: URLProtocol {
    private static let lock = NSLock()
    private static var recorded: [String] = []

    static var attempts: [String] {
        lock.lock(); defer { lock.unlock() }
        return recorded
    }

    override class func canInit(with request: URLRequest) -> Bool {
        lock.lock(); defer { lock.unlock() }
        recorded.append(request.url?.absoluteString ?? "?")
        return true
    }

    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
    }

    override func stopLoading() {}

    private static var installed = false

    static func install() {
        guard !installed else { return }
        installed = true
        URLProtocol.registerClass(NetworkGuard.self)
        for name in ["defaultSessionConfiguration", "ephemeralSessionConfiguration"] {
            swizzle(name)
        }
    }

    private static func swizzle(_ name: String) {
        let original = class_getClassMethod(URLSessionConfiguration.self, NSSelectorFromString(name))!
        let originalIMP = method_getImplementation(original)
        typealias Factory = @convention(c) (AnyClass, Selector) -> URLSessionConfiguration
        let make = unsafeBitCast(originalIMP, to: Factory.self)
        let block: @convention(block) (AnyClass) -> URLSessionConfiguration = { _ in
            let config = make(URLSessionConfiguration.self, NSSelectorFromString(name))
            config.protocolClasses = [NetworkGuard.self] + (config.protocolClasses ?? [])
            return config
        }
        method_setImplementation(original, imp_implementationWithBlock(block))
    }
}
