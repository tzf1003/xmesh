// Read-only independent acceptance observer. Never opens a window or sends input.
import AppKit
import ApplicationServices
import CoreGraphics
import Dispatch
import Foundation

// Accessibility queries into the foreground app can block for over a second.
// They run on their own thread, so the 15 ms pointer and frontmost-app sample
// keeps publishing while a query is slow. The file reports how long ago the
// last focus sample completed, and the harness fails the run past its limit,
// because a focus change during a stalled query cannot be seen.
final class FocusSampler {
    private let lock=NSLock()
    private let system=AXUIElementCreateSystemWide()
    private var target:pid_t=0
    private var generation=0
    private var available=false
    private var previousElement:AXUIElement?
    private var previousWindow:AXUIElement?
    // Monotonic time of the last completed sample. Initialization counts as the
    // first completion, so startup is not reported as a stall.
    private var completedNanoseconds=DispatchTime.now().uptimeNanoseconds

    init(){
        AXUIElementSetMessagingTimeout(system,0.025)
        let thread=Thread{[self] in self.run()}
        thread.name="xmesh.observer.focus"
        thread.start()
    }

    func setTarget(_ pid:pid_t){
        lock.lock();target=pid;lock.unlock()
    }

    // Age is measured from the last completed sample, so a slow or blocked
    // Accessibility query grows it even while the generation stays unchanged.
    func snapshot()->(generation:Int,available:Bool,ageMilliseconds:Double){
        lock.lock();defer{lock.unlock()}
        let now=DispatchTime.now().uptimeNanoseconds
        let elapsed=now>=completedNanoseconds ? now-completedNanoseconds : 0
        return (generation,available,Double(elapsed)/1_000_000)
    }

    private func run(){
        while true{
            lock.lock();let pid=target;lock.unlock()
            if pid != 0 {sample(pid)}
            Thread.sleep(forTimeInterval:0.05)
        }
    }

    private func sample(_ pid:pid_t){
        // Keep only opaque AX identity. Never query text, titles,
        // selections or values in the user's foreground application.
        let app=AXUIElementCreateApplication(pid)
        let focused=element(system,kAXFocusedUIElementAttribute)
        let window=element(app,kAXFocusedWindowAttribute)
        var focusedPID:pid_t=0
        let isAvailable=focused.map{AXUIElementGetPid($0,&focusedPID) == .success} == true && focusedPID==pid && window != nil
        lock.lock();defer{lock.unlock()}
        if isAvailable != available || !equal(focused,previousElement) || !equal(window,previousWindow){generation+=1}
        previousElement=focused;previousWindow=window;available=isAvailable
        completedNanoseconds=DispatchTime.now().uptimeNanoseconds
    }

    private func element(_ object:AXUIElement,_ attribute:String)->AXUIElement?{
        var value:CFTypeRef?
        guard AXUIElementCopyAttributeValue(object,attribute as CFString,&value) == .success,
              let value,CFGetTypeID(value)==AXUIElementGetTypeID() else{return nil}
        return (value as! AXUIElement)
    }

    private func equal(_ lhs:AXUIElement?,_ rhs:AXUIElement?)->Bool{
        switch (lhs,rhs){case (nil,nil):return true;case let (lhs?,rhs?):return CFEqual(lhs,rhs);default:return false}
    }
}

@main struct DesktopObserver{
    static func main(){
        guard CommandLine.arguments.count==2 else{exit(2)}
        NSApplication.shared.setActivationPolicy(.prohibited)
        NSApplication.shared.finishLaunching()
        let workspace=NSWorkspace.shared
        let file=URL(fileURLWithPath:CommandLine.arguments[1])
        let sampler=FocusSampler()
        let timer=Timer.scheduledTimer(withTimeInterval:0.015,repeats:true){_ in
            guard let front=workspace.frontmostApplication,!front.isTerminated,
                  let point=CGEvent(source:nil)?.location else{return}
            sampler.setTarget(front.processIdentifier)
            let focus=sampler.snapshot()
            let value:[String:Any]=["pid":front.processIdentifier,"x":point.x,"y":point.y,"app_name":front.executableURL?.lastPathComponent ?? "","focus_generation":focus.generation,"focus_available":focus.available,"focus_sample_age_ms":focus.ageMilliseconds]
            if let data=try? JSONSerialization.data(withJSONObject:value){try? data.write(to:file,options:.atomic)}
        }
        withExtendedLifetime(timer){CFRunLoopRun()}
    }
}
