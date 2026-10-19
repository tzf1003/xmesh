// Read-only independent acceptance observer. Never opens a window or sends input.
import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

@main struct DesktopObserver{
    static func main(){
        guard CommandLine.arguments.count==2 else{exit(2)}
        NSApplication.shared.setActivationPolicy(.prohibited)
        NSApplication.shared.finishLaunching()
        let workspace=NSWorkspace.shared
        let file=URL(fileURLWithPath:CommandLine.arguments[1])
        let system=AXUIElementCreateSystemWide()
        AXUIElementSetMessagingTimeout(system,0.025)
        var previousElement:AXUIElement?
        var previousWindow:AXUIElement?
        var focusGeneration=0
        var focusAvailable=false
        var nextFocusSample=0.0
        nonisolated func element(_ object:AXUIElement,_ attribute:String)->AXUIElement?{
            var value:CFTypeRef?
            guard AXUIElementCopyAttributeValue(object,attribute as CFString,&value) == .success,
                  let value,CFGetTypeID(value)==AXUIElementGetTypeID() else{return nil}
            return (value as! AXUIElement)
        }
        nonisolated func equal(_ lhs:AXUIElement?,_ rhs:AXUIElement?)->Bool{
            switch (lhs,rhs){case (nil,nil):return true;case let (lhs?,rhs?):return CFEqual(lhs,rhs);default:return false}
        }
        let timer=Timer.scheduledTimer(withTimeInterval:0.015,repeats:true){_ in
            guard let front=workspace.frontmostApplication,!front.isTerminated,
                  let point=CGEvent(source:nil)?.location else{return}
            let now=ProcessInfo.processInfo.systemUptime
            if now>=nextFocusSample{
                nextFocusSample=now+0.05
                // Keep only opaque AX identity. Never query text, titles,
                // selections or values in the user's foreground application.
                let app=AXUIElementCreateApplication(front.processIdentifier)
                let focused=element(system,kAXFocusedUIElementAttribute)
                let window=element(app,kAXFocusedWindowAttribute)
                var focusedPID:pid_t=0
                let available=focused.map{AXUIElementGetPid($0,&focusedPID) == .success} == true && focusedPID==front.processIdentifier && window != nil
                if available != focusAvailable || !equal(focused,previousElement) || !equal(window,previousWindow){focusGeneration+=1}
                previousElement=focused;previousWindow=window;focusAvailable=available
            }
            let value:[String:Any]=["pid":front.processIdentifier,"x":point.x,"y":point.y,"app_name":front.executableURL?.lastPathComponent ?? "","focus_generation":focusGeneration,"focus_available":focusAvailable]
            if let data=try? JSONSerialization.data(withJSONObject:value){try? data.write(to:file,options:.atomic)}
        }
        withExtendedLifetime(timer){CFRunLoopRun()}
    }
}
