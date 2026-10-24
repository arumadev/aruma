// swift-tools-version: 5.9
// The e2e plugin's iOS half, with ArumaRedirect from this repository. Flutter reaches this
// manifest through a symlink in ios/Flutter/ephemeral/Packages/.packages, so the repository is
// found from the manifest's real path.

import Foundation
import PackageDescription

let repo = URL(fileURLWithPath: #filePath).resolvingSymlinksInPath()
  .deletingLastPathComponent()
  .appendingPathComponent("../../../../../..").standardized

let package = Package(
  name: "aruma_e2e_native",
  platforms: [
    .iOS("15.0")
  ],
  products: [
    .library(name: "aruma-e2e-native", targets: ["aruma_e2e_native"])
  ],
  dependencies: [
    .package(name: "ArumaRedirect", path: repo.appendingPathComponent("sdk/ios/ArumaRedirect").path),
    .package(name: "FlutterFramework", path: "../FlutterFramework"),
  ],
  targets: [
    .target(
      name: "aruma_e2e_native",
      dependencies: [
        .product(name: "ArumaRedirect", package: "ArumaRedirect"),
        .product(name: "FlutterFramework", package: "FlutterFramework"),
      ]
    )
  ]
)
