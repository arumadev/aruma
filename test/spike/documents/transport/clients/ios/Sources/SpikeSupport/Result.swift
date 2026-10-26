import Foundation

/// Prints one result line, "RESULT <label> <check> PASS|FAIL …", which run.sh collects from the
/// xcodebuild log.
public func spikeResult(_ label: String, _ check: String, _ outcome: String) {
  print("RESULT \(label) \(check) \(outcome)")
}
