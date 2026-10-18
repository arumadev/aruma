// swift-tools-version:5.9
// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import PackageDescription

let package = Package(
  name: "ArumaRedirect",
  platforms: [.iOS(.v13), .macOS(.v10_15), .tvOS(.v13)],
  products: [
    // Both targets: ArumaRedirectLoader installs the redirect at launch from Info.plist (ArumaURL),
    // so an app that adds this product needs no code.
    .library(name: "ArumaRedirect", targets: ["ArumaRedirect", "ArumaRedirectLoader"]),
  ],
  targets: [
    .target(name: "ArumaRedirect", path: "Sources/ArumaRedirect"),
    .target(
      name: "ArumaRedirectLoader",
      dependencies: ["ArumaRedirect"],
      path: "Sources/ArumaRedirectLoader"
    ),
    .testTarget(
      name: "ArumaRedirectTests",
      dependencies: ["ArumaRedirect", "ArumaRedirectLoader"],
      path: "Tests/ArumaRedirectTests"
    ),
  ]
)
