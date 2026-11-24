import SwiftUI

/// The test host: an app with nothing in it. The tests run inside it (see project.yml).
@main
struct HostApp: App {
  var body: some Scene {
    WindowGroup { Text("Aruma Document Database e2e") }
  }
}
