import SwiftUI

/// The test host: an app with nothing in it. The tests run inside it (see project.yml).
@main
struct HostApp: App {
  var body: some Scene {
    WindowGroup { Text("Aruma Realtime Database e2e") }
  }
}
