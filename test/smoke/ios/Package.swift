// swift-tools-version: 5.9
// Initialises FirebaseCore and FirebaseInstallations from Aruma's GoogleService-Info.plist on an
// iOS simulator, with all network access blocked. Run by test/smoke/run.sh.
import PackageDescription

let package = Package(
    name: "ArumaSmokeIOS",
    platforms: [.iOS(.v15)],
    dependencies: [
        // 12.x is the newest line that builds with Xcode 16.
        .package(url: "https://github.com/firebase/firebase-ios-sdk", "12.0.0"..<"13.0.0"),
    ],
    targets: [
        // Swift cannot catch NSException, which FirebaseCore raises on an invalid config.
        .target(name: "ExceptionCatcher", path: "Sources/ExceptionCatcher"),
        .testTarget(
            name: "ArumaSmokeTests",
            dependencies: [
                "ExceptionCatcher",
                .product(name: "FirebaseCore", package: "firebase-ios-sdk"),
                .product(name: "FirebaseInstallations", package: "firebase-ios-sdk"),
            ],
            path: "Tests/ArumaSmokeTests",
            resources: [.copy("Fixtures")]
        ),
    ]
)
