# Copyright 2026 The Aruma Authors
# SPDX-License-Identifier: Apache-2.0

Pod::Spec.new do |s|
  s.name         = 'ArumaRedirect'
  s.version      = '0.1.0'
  s.summary      = 'Sends the official Firebase iOS Auth SDK to an Aruma server, with no app code.'
  s.description  = <<-DESC
    A URLProtocol that rewrites the Firebase Auth endpoints (identitytoolkit, securetoken and the
    v3 relyingparty paths), and Cloud Storage as a fallback, to the Aruma server named by ArumaURL
    in Info.plist. Other hosts are untouched. Scripts/aruma_storage.rb patches the FirebaseStorage pod.
  DESC
  s.homepage     = 'https://github.com/arumadev/aruma/tree/main/sdk/ios/ArumaRedirect'
  s.license      = { :type => 'Apache-2.0', :file => 'LICENSE' }
  s.author       = { 'The Aruma Authors' => 'noreply@aruma.dev' }
  s.source       = { :git => 'https://github.com/arumadev/aruma.git', :tag => "ios-redirect-#{s.version}" }

  s.ios.deployment_target  = '13.0'
  s.osx.deployment_target  = '10.15'
  s.tvos.deployment_target = '13.0'
  s.swift_version = '5.9'

  # The Objective-C +load in ArumaRedirectLoader.m performs the install at launch.
  s.source_files        = 'Sources/ArumaRedirect/**/*.swift', 'Sources/ArumaRedirectLoader/**/*.{h,m}'
  s.public_header_files = 'Sources/ArumaRedirectLoader/include/*.h'
  s.frameworks          = 'Foundation'
  s.preserve_paths      = 'Scripts/aruma_storage.rb'
end
