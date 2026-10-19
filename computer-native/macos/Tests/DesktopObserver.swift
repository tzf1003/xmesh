// Read-only independent acceptance observer. Never opens a window or sends input.
import AppKit
import CoreGraphics
import Foundation

@main struct DesktopObserver{
    static func main(){
        guard CommandLine.arguments.count==2 else{exit(2)}
        NSApplication.shared.setActivationPolicy(.prohibited)
        NSApplication.shared.finishLaunching()
        let workspace=NSWorkspace.shared
        let file=URL(fileURLWithPath:CommandLine.arguments[1])
        let timer=Timer.scheduledTimer(withTimeInterval:0.015,repeats:true){_ in
            guard let front=workspace.frontmostApplication,!front.isTerminated,
                  let point=CGEvent(source:nil)?.location else{return}
            let value:[String:Any]=["pid":front.processIdentifier,"x":point.x,"y":point.y,"app_name":front.executableURL?.lastPathComponent ?? ""]
            if let data=try? JSONSerialization.data(withJSONObject:value){try? data.write(to:file,options:.atomic)}
        }
        withExtendedLifetime(timer){CFRunLoopRun()}
    }
}
