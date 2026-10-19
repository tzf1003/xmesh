// Background-only application entry point for the isolated DMG upgrade test.
// The bundled kernel and desktop executor are the actual xmesh binaries.
import AppKit
import Foundation

@main struct UpgradeFixture {
    static func main() throws {
        NSApplication.shared.setActivationPolicy(.prohibited)
        let info=Bundle.main.infoDictionary ?? [:]
        guard let receipt=info["XmeshUpgradeReceipt"] as? String,
              let version=info["CFBundleVersion"] as? String else{exit(2)}
        let value:[String:Any]=["version":version,"pid":ProcessInfo.processInfo.processIdentifier]
        try JSONSerialization.data(withJSONObject:value).write(to:URL(fileURLWithPath:receipt),options:.atomic)
        if info["XmeshUpgradeWait"] as? Bool == true {CFRunLoopRun()}
    }
}
