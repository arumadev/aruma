// swift-tools-version: 5.9
// The official FirebaseFirestore (iOS) against the Document Database transport spike server, on an
// iOS simulator: Firestore.settings.host and isSSLEnabled, nothing else. Run by ../ios/run.sh, on
// CI only (.github/workflows/documents-spike.yml).
import PackageDescription

let package = Package(
    name: "TransportSpikeIOS",
    platforms: [.iOS(.v15)],
    dependencies: [
        // 12.x is the newest line that builds with Xcode 16 (as test/smoke/ios).
        .package(url: "https://github.com/firebase/firebase-ios-sdk", "12.0.0"..<"13.0.0"),
    ],
    targets: [
        // The results the tests print, in the form run.sh reads.
        .target(name: "SpikeSupport", path: "Sources/SpikeSupport"),
        .testTarget(
            name: "TransportSpikeTests",
            dependencies: [
                "SpikeSupport",
                .product(name: "FirebaseFirestore", package: "firebase-ios-sdk"),
            ],
            path: "Tests/TransportSpikeTests"
        ),
    ]
)
