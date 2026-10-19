import AppKit
import ApplicationServices
import CoreGraphics
import CoreServices
import Foundation

// The pinned upstream dispatchers call these hooks at every delivery boundary.
// The Rust executor supplies the lease, deadline, permission and target guard.
public typealias SessionCheck = @convention(c) (UnsafeMutableRawPointer?, Bool) -> Bool
private var sessionCheck: SessionCheck?
private var sessionContext: UnsafeMutableRawPointer?
private var releasingInput = false
private var sessionTargetPID: pid_t?
private var sessionTarget: Action?
private let bridgeLock = NSLock()
private enum BridgeStop: Error { case userIntervention, targetChanged, keyboardTargetUnfocused }

func xmeshValidateDelivery(cleanup:Bool,authority:()->Bool,window:()->Bool,intervention:()->Bool)throws{
    guard authority() else{throw ComputerUseError.stateUnavailable("session_cancelled")}
    // The Rust cleanup callback keeps the process-start fence while relaxing
    // lease/permission checks. Releases must also survive window disappearance.
    if cleanup{return}
    guard window() else{throw BridgeStop.targetChanged}
    if intervention(){throw BridgeStop.userIntervention}
}

func requireUsableComputerUseSession() throws {
    try xmeshValidateDelivery(cleanup:releasingInput,authority:{sessionCheck?(sessionContext,releasingInput) == true},window:{
        guard let target=sessionTarget else{return true}
        let windows=currentWindowInfo()
        return skyKeyWindowMatchesTarget(windowInfo:windows,windowID:target.window,pid:target.pid) && targetMatchesWindow(target,windows)
    },intervention:{
        guard let target=sessionTargetPID else{return false}
        return xmeshFrontmostPID()==target
    })
}

func xmeshReleaseInput(_ operation:()->Void){
    let previous=releasingInput;releasingInput=true
    defer{releasingInput=previous}
    guard (try? requireUsableComputerUseSession()) != nil else{return}
    operation()
}

enum TimingLog {
    static func now() -> TimeInterval { ProcessInfo.processInfo.systemUptime }
    static func log(_ label: String, since start: TimeInterval) { /* xmesh owns metadata-only audit. */ }
}

enum InputTiming {
    static let skyClickDelayScale = 1.0
    static let focusRecordSettle = 0.010
    static let typeChunkDelay = 0.008
    static let pressKeySettle = 0.010
    static func milliseconds(_ name: String, default value: Double) -> TimeInterval {value/1000}
}

struct Action: Decodable {
    let type: String
    let pid: Int32
    let window: UInt32
    let originX: Double
    let originY: Double
    let width: Double
    let height: Double
    let x: Double?
    let y: Double?
    let toX: Double?
    let toY: Double?
    let clicks: Int?
    let button: String?
    let deltaX: Int?
    let deltaY: Int?
    let durationMS: Int?
    let keys: [String]?
    let text: String?

    var bounds: CGRect { CGRect(x: originX, y: originY, width: width, height: height) }
    func point(_ x: Double?, _ y: Double?) throws -> CGPoint {
        guard let x, let y, x.isFinite, y.isFinite, x >= 0, y >= 0, x < width, y < height else { throw ComputerUseError.invalidArguments("coordinates") }
        return CGPoint(x: originX + x, y: originY + y)
    }
}

@_cdecl("xmesh_background_available")
public func xmeshBackgroundAvailable() -> Int32 {
    SkyLightSPI.shared.capability.isAvailable ? 1 : 0
}

@_cdecl("xmesh_background_initialize")
public func xmeshBackgroundInitialize(){
    // The native executor has no windows. Initialize AppKit on its main thread
    // with activation prohibited before capture or input libraries register it.
    NSApplication.shared.setActivationPolicy(.prohibited)
    var app=Bundle.main.bundleURL
    while app.path != "/"{
        if app.pathExtension=="app"{_ = LSRegisterURL(app as CFURL,true)}
        app.deleteLastPathComponent()
    }
    // This sets only this executor's default AX timeout. An application AX
    // object's timeout does not apply to the child objects returned by AX.
    AXUIElementSetMessagingTimeout(AXUIElementCreateSystemWide(),0.1)
}

@_cdecl("xmesh_background_run_loop")
public func xmeshBackgroundRunLoop(){
    NSApplication.shared.finishLaunching()
    CFRunLoopRun()
}

@_cdecl("xmesh_background_stop_loop")
public func xmeshBackgroundStopLoop(){
    DispatchQueue.main.async{CFRunLoopStop(CFRunLoopGetMain())}
}

@_cdecl("xmesh_background_event_target")
public func xmeshBackgroundEventTarget(_ raw:UnsafeMutableRawPointer?,_ kind:UInt32)->Int32 {
    guard let raw else{return 0}
    let event=Unmanaged<CGEvent>.fromOpaque(raw).takeUnretainedValue()
    if [UInt32(1),3,25].contains(kind){
        let point=event.location
        let windows=CGWindowListCopyWindowInfo([.optionOnScreenOnly,.excludeDesktopElements],kCGNullWindowID) as? [[String:Any]] ?? []
        for window in windows {
            guard (window[kCGWindowLayer as String] as? Int)==0,
                  let bounds=window[kCGWindowBounds as String] as? [String:Any],
                  let rect=CGRect(dictionaryRepresentation:bounds as CFDictionary),rect.contains(point),
                  let pid=window[kCGWindowOwnerPID as String] as? Int32 else{continue}
            return pid
        }
    }
    return xmeshFrontmostPID() ?? 0
}

@_cdecl("xmesh_background_dispatch")
public func xmeshBackgroundDispatch(_ bytes: UnsafePointer<UInt8>?, _ count: Int, _ check: SessionCheck?, _ context: UnsafeMutableRawPointer?) -> Int32 {
    // Delivery runs on Rust's serial input worker. Keep AppKit's main loop free
    // to refresh foreground identity and service read-only monitor queries.
    guard let bytes, count > 0, count <= 1_048_576, let check else { return 1 }
    bridgeLock.lock()
    defer { bridgeLock.unlock() }
    sessionCheck = check
    sessionContext = context
    defer { sessionCheck = nil; sessionContext = nil; releasingInput = false; sessionTargetPID = nil;sessionTarget=nil }
    do {
        let action = try JSONDecoder().decode(Action.self, from: Data(bytes: bytes, count: count))
        sessionTargetPID=action.pid
        sessionTarget=action
        guard action.pid > 0, action.window > 0, action.bounds.width > 0, action.bounds.height > 0,
              [action.originX, action.originY, action.width, action.height].allSatisfy(\.isFinite)
        else { return 1 }
        try requireUsableComputerUseSession()
        let before = frontmostPID()
        guard before != nil, before != action.pid else { return 3 }
        let windowInfo = CGWindowListCopyWindowInfo([.optionAll], kCGNullWindowID) as? [[String: Any]] ?? []
        guard skyKeyWindowMatchesTarget(windowInfo: windowInfo, windowID: action.window, pid: action.pid) else { return 4 }
        switch action.type {
        case "click":
            let point = try action.point(action.x, action.y)
            let semantic=try xmeshAccessibilityClick(pid:action.pid,window:action.window,point:point,button:action.button ?? "left",count:action.clicks ?? 1)
            if semantic{break}
            guard (action.button ?? "left") == "left", (1...2).contains(action.clicks ?? 1) else { return 5 }
            do {try SkyClickDispatcher.click(target: SkyClickTarget(
                screenPoint: point,
                windowPoint: CGPoint(x: point.x-action.originX, y: point.y-action.originY),
                windowBounds: action.bounds, windowID: action.window, pid: action.pid
            ), clickCount: action.clicks ?? 1)}
            catch {try? releasePointer(action,point);throw error}
        case "text":
            guard let text=action.text, !text.isEmpty, text.utf8.count<=65536 else {return 1}
            if try xmeshAccessibilityText(pid:action.pid,window:action.window,text:text){break}
            try SkyKeyboardDispatcher.typeText(target: SkyKeyboardTarget(windowID: action.window, pid: action.pid), text: text)
        case "key":
            guard let keys=action.keys, !keys.isEmpty, keys.count<=8 else{return 1}
            if try xmeshAccessibilitySelectAll(pid:action.pid,window:action.window,keys:keys){break}
            let normalized=keys.map { key -> String in
                switch key.lowercased() {case "delete":return "forwarddelete";case "space":return "space";default:return key}
            }.joined(separator:"+")
            try SkyKeyboardDispatcher.pressKey(target: SkyKeyboardTarget(windowID:action.window,pid:action.pid),key:normalized)
        case "move":
            let point=try action.point(action.x,action.y)
            guard let source=CGEventSource(stateID:.privateState),
                  let event=CGEvent(mouseEventSource:source,mouseType:.mouseMoved,mouseCursorPosition:point,mouseButton:.left) else{return 2}
            try stamp(event,point,action)
            try SkyLightSPI.shared.postToPid(event,pid:action.pid)
        case "scroll":
            try withTargetFocus(action) {try dispatchPointer(action)}
        case "drag":
            try withTargetFocus(action) { try dispatchPointer(action) }
        default:return 5
        }
        // A user may switch between unrelated apps while this action runs.
        // Only activation of the controlled app is an interference boundary;
        // another foreground app is normal concurrent user activity.
        guard let after=frontmostPID() else{return 6}
        guard after != action.pid else {return 6}
        return 0
    } catch BridgeStop.userIntervention {return 3}
      catch BridgeStop.targetChanged {return 4}
      catch BridgeStop.keyboardTargetUnfocused {return 5}
      catch { return 2 }
}

private func currentWindowInfo()->[[String:Any]]{
    CGWindowListCopyWindowInfo([.optionAll],kCGNullWindowID) as? [[String:Any]] ?? []
}

func targetMatchesWindow(_ target:Action,_ windows:[[String:Any]])->Bool{
    windows.contains{window in
        guard let number=window[kCGWindowNumber as String] as? NSNumber,number.uint32Value==target.window,
              let owner=window[kCGWindowOwnerPID as String] as? NSNumber,owner.int32Value==target.pid,
              let bounds=window[kCGWindowBounds as String] as? [String:Any],
              let rect=CGRect(dictionaryRepresentation:bounds as CFDictionary) else{return false}
        return rect==target.bounds
    }
}

func xmeshFrontmostPID() -> pid_t? {
    // The executor services AppKit's main run loop. Query the workspace on that
    // thread so its KVO state advances and window z-order is not mistaken for
    // keyboard focus (nonactivating windows can be above the active app).
    let query:()->pid_t?={
        guard let app=NSWorkspace.shared.frontmostApplication,!app.isTerminated else{return nil}
        return app.processIdentifier
    }
    return Thread.isMainThread ? query() : DispatchQueue.main.sync(execute:query)
}

private func frontmostPID() -> pid_t? { xmeshFrontmostPID() }

// Adapted from open-computer-use WindowOcclusionKeepAlive.swift's framework
// detector at the pinned revision. Native AppKit and Chromium need different
// keyboard focus recipes; the decision precedes any input mutation.
func xmeshRequiresSyntheticKeyboardFocus(frameworkNames:[String])->Bool{
    frameworkNames.contains{let name=$0.lowercased();return name.contains("chrom") || name.contains("electron") || name.contains("cef")}
}

func xmeshChromiumApp(_ pid:pid_t)->Bool{
    let frameworks=NSRunningApplication(processIdentifier:pid)?.bundleURL?.appendingPathComponent("Contents/Frameworks")
    let names=frameworks.flatMap{try? FileManager.default.contentsOfDirectory(atPath:$0.path)} ?? []
    return xmeshRequiresSyntheticKeyboardFocus(frameworkNames:names)
}

func xmeshUseDirectKeyboard(_ pid:pid_t,window:CGWindowID)throws->Bool{
    if xmeshChromiumApp(pid){return false}
    try requireUsableComputerUseSession()
    guard xmeshAccessibilityKeyboardTarget(pid:pid,window:window) else{throw BridgeStop.keyboardTargetUnfocused}
    return true
}

func xmeshShouldEndSyntheticFocus(targetPID:pid_t,frontmostPID:pid_t?)->Bool{
    guard let frontmostPID else{return false}
    return frontmostPID != targetPID
}

func xmeshEndSyntheticTargetFocus(_ spi:SkyLightSPI,_ context:SkyLightSyntheticFocusContext,pid:pid_t)throws{
    // Synthetic activation ends only while the user remains in another app.
    // Releasing held keys/buttons is independent and still runs on cancellation.
    guard xmeshShouldEndSyntheticFocus(targetPID:pid,frontmostPID:xmeshFrontmostPID()) else{return}
    try spi.endSyntheticTargetFocus(context)
}

private func withTargetFocus(_ action:Action,_ operation:() throws -> Void) throws {
    let spi=SkyLightSPI.shared
    let focus=try spi.beginSyntheticTargetFocus(targetPID:action.pid,targetWindowID:action.window)
    do {
        // Mouse delivery uses the upstream click path's target-active record.
        // Key-window records are reserved for the keyboard dispatcher.
        try operation()
        Thread.sleep(forTimeInterval:SkyKeyboardDispatcher.releaseSettle)
    } catch {try? xmeshEndSyntheticTargetFocus(spi,focus,pid:action.pid);throw error}
    try xmeshEndSyntheticTargetFocus(spi,focus,pid:action.pid)
}

// Adapted from minghinmatthewlam/computer-use-mcp Input.swift at
// 82dd8093e44819c51537f489059e172777d7f1d7 (MIT, Copyright 2026 Matthew Lam).
// Changes: synchronous lease guard, fixed window affinity, private SkyLight
// routing and finite xmesh action budget. No process/global fallback is selected.
private func dispatchPointer(_ action:Action) throws {
    let start=try action.point(action.x,action.y)
    // Adapted from open-computer-use appHasLazyWebAccessibility: detect the
    // renderer by shipped frameworks instead of an application-name list.
    let chromium=xmeshChromiumApp(action.pid)
    guard let source=CGEventSource(stateID:chromium ? .hidSystemState : .privateState) else{throw ComputerUseError.message("event_source")}
    source.localEventsSuppressionInterval=0
    func post(_ type:CGEventType,_ point:CGPoint,_ cleanup:Bool=false) throws {
        let wasReleasing=releasingInput
        if cleanup{releasingInput=true}
        defer{releasingInput=wasReleasing}
        try requireUsableComputerUseSession()
        guard let event=CGEvent(mouseEventSource:source,mouseType:type,mouseCursorPosition:point,mouseButton:.left) else{throw ComputerUseError.message("event")}
        try stamp(event,point,action)
        if chromium{try SkyLightSPI.shared.postToPid(event,pid:action.pid)}
        event.postToPid(action.pid)
    }
    if action.type=="scroll"{
        let dx=action.deltaX ?? 0,dy=action.deltaY ?? 0
        guard abs(dx)<=10000,abs(dy)<=10000 else{throw ComputerUseError.invalidArguments("scroll")}
        // Reuse computer-use-mcp's pixel/phase recipe. A xmesh discrete step is
        // represented as 40 pixels; the native scroll event remains PID-bound.
        func postScroll(_ x:Int,_ y:Int,_ phase:CGScrollPhase)throws{
            try requireUsableComputerUseSession()
            guard let event=CGEvent(scrollWheelEvent2Source:source,units:.pixel,wheelCount:2,wheel1:Int32(-y),wheel2:Int32(-x),wheel3:0) else{throw ComputerUseError.message("event")}
            event.location=start
            try stamp(event,start,action)
            event.setIntegerValueField(.scrollWheelEventPointDeltaAxis1,value:Int64(-y))
            event.setIntegerValueField(.scrollWheelEventPointDeltaAxis2,value:Int64(-x))
            event.setIntegerValueField(.scrollWheelEventFixedPtDeltaAxis1,value:Int64(-y*65536))
            event.setIntegerValueField(.scrollWheelEventFixedPtDeltaAxis2,value:Int64(-x*65536))
            event.setIntegerValueField(.scrollWheelEventScrollPhase,value:Int64(phase.rawValue))
            event.setIntegerValueField(.scrollWheelEventMomentumPhase,value:Int64(CGMomentumScrollPhase.none.rawValue))
            event.setIntegerValueField(.scrollWheelEventScrollCount,value:1)
            event.setIntegerValueField(.scrollWheelEventIsContinuous,value:1)
            event.setIntegerValueField(.mouseEventWindowUnderMousePointer,value:Int64(action.window))
            event.setIntegerValueField(.mouseEventWindowUnderMousePointerThatCanHandleThisEvent,value:Int64(action.window))
            event.postToPid(action.pid)
        }
        try postScroll(dx*40,dy*40,.began)
        try postScroll(0,0,.ended)
        return
    }
    guard (action.button ?? "left")=="left" else{throw ComputerUseError.invalidArguments("button")}
    let end=try action.point(action.toX,action.toY)
    if chromium{
        let group=Int64(DispatchTime.now().uptimeNanoseconds % 1_000_000_000)
        for step in try skyClickEventRecipe(clickCount:1).prefix(3){
            try requireUsableComputerUseSession()
            let point=step.pointKind == .target ? start : CGPoint(x:-1,y:-1)
            guard let event=CGEvent(mouseEventSource:source,mouseType:step.kind.cgEventType,mouseCursorPosition:point,mouseButton:.left) else{throw ComputerUseError.message("event")}
            try stamp(event,point,action)
            for (field,value) in [(UInt32(0),step.phase),(1,step.clickState),(3,Int64(0)),(7,Int64(3)),(58,group)]{try SkyLightSPI.shared.setIntegerField(event,field:field,value:value)}
            if step.pointKind == .primer{try SkyLightSPI.shared.setWindowLocation(event,point:CGPoint(x:-1,y:-1))}
            try SkyLightSPI.shared.postToPid(event,pid:action.pid)
            event.postToPid(action.pid)
            Thread.sleep(forTimeInterval:step.delayAfter)
        }
    }
    var aborted=true
    defer{try? post(.leftMouseUp,aborted ? start : end,true)}
    try post(.leftMouseDown,start)
    var previous=start
    for step in 1...24{
        try requireUsableComputerUseSession()
        let t=Double(step)/24
        let point=CGPoint(x:start.x+(end.x-start.x)*t,y:start.y+(end.y-start.y)*t)
        guard let event=CGEvent(mouseEventSource:source,mouseType:.leftMouseDragged,mouseCursorPosition:point,mouseButton:.left) else{throw ComputerUseError.message("event")}
        event.setDoubleValueField(.mouseEventDeltaX,value:point.x-previous.x)
        event.setDoubleValueField(.mouseEventDeltaY,value:point.y-previous.y)
        event.setIntegerValueField(.mouseEventDeltaX,value:Int64((point.x-previous.x).rounded()))
        event.setIntegerValueField(.mouseEventDeltaY,value:Int64((point.y-previous.y).rounded()))
        try stamp(event,point,action)
        if chromium{try SkyLightSPI.shared.postToPid(event,pid:action.pid)}
        event.postToPid(action.pid)
        previous=point
        Thread.sleep(forTimeInterval:Double(min(2000,max(200,action.durationMS ?? 300)))/24000)
    }
    aborted=false
}

private func stamp(_ event:CGEvent,_ point:CGPoint,_ action:Action) throws {
    for (field,value) in [(UInt32(40),Int64(action.pid)),(51,Int64(action.window)),(91,Int64(action.window)),(92,Int64(action.window)),(42,Int64(0x584d5348))]{
        try SkyLightSPI.shared.setIntegerField(event,field:field,value:value)
    }
    try SkyLightSPI.shared.setWindowLocation(event,point:CGPoint(x:point.x-action.originX,y:point.y-action.originY))
}

private func releasePointer(_ action:Action,_ point:CGPoint) throws{
    let previous=releasingInput;releasingInput=true
    defer{releasingInput=previous}
    try requireUsableComputerUseSession()
    guard let source=CGEventSource(stateID:.privateState),
          let event=CGEvent(mouseEventSource:source,mouseType:.leftMouseUp,mouseCursorPosition:point,mouseButton:.left) else{return}
    try stamp(event,point,action)
    try SkyLightSPI.shared.postToPid(event,pid:action.pid)
    event.postToPid(action.pid)
}
