import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

// The pinned upstream dispatchers call these hooks at every delivery boundary.
// The Rust executor supplies the lease, deadline, permission and target guard.
public typealias SessionCheck = @convention(c) (UnsafeMutableRawPointer?) -> Bool
private var sessionCheck: SessionCheck?
private var sessionContext: UnsafeMutableRawPointer?
private var releasingInput = false
private var sessionTargetPID: pid_t?
private let bridgeLock = NSLock()
private enum BridgeStop: Error { case userIntervention }

func requireUsableComputerUseSession() throws {
    guard releasingInput || sessionCheck?(sessionContext) == true else { throw ComputerUseError.stateUnavailable("session_cancelled") }
    if !releasingInput, let target=sessionTargetPID, xmeshFrontmostPID()==target {throw BridgeStop.userIntervention}
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
    static func milliseconds(_ name: String, default value: Double) -> TimeInterval { value / 1000 }
}

private struct Action: Decodable {
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
    guard let bytes, count > 0, count <= 1_048_576, let check else { return 1 }
    bridgeLock.lock()
    defer { bridgeLock.unlock() }
    sessionCheck = check
    sessionContext = context
    defer { sessionCheck = nil; sessionContext = nil; releasingInput = false; sessionTargetPID = nil }
    do {
        let action = try JSONDecoder().decode(Action.self, from: Data(bytes: bytes, count: count))
        sessionTargetPID=action.pid
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
            guard (action.button ?? "left") == "left", (1...2).contains(action.clicks ?? 1) else { return 5 }
            let point = try action.point(action.x, action.y)
            do {try SkyClickDispatcher.click(target: SkyClickTarget(
                screenPoint: point,
                windowPoint: CGPoint(x: point.x-action.originX, y: point.y-action.originY),
                windowBounds: action.bounds, windowID: action.window, pid: action.pid
            ), clickCount: action.clicks ?? 1)}
            catch {try? releasePointer(action,point);throw error}
        case "text":
            guard let text=action.text, !text.isEmpty, text.utf8.count<=65536 else {return 1}
            try SkyKeyboardDispatcher.typeText(target: SkyKeyboardTarget(windowID: action.window, pid: action.pid), text: text)
        case "key":
            guard let keys=action.keys, !keys.isEmpty, keys.count<=8 else{return 1}
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
        case "drag", "scroll":
            try withTargetFocus(action) { try dispatchPointer(action) }
        default:return 5
        }
        guard frontmostPID() == before else {return 6}
        return 0
    } catch BridgeStop.userIntervention {return 3}
      catch { return 2 }
}

func xmeshFrontmostPID() -> pid_t? {
    // AX asks the current WindowServer state rather than a run-loop cached
    // NSWorkspace value. No app is activated or brought forward by this query.
    let system=AXUIElementCreateSystemWide()
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(system,kAXFocusedApplicationAttribute as CFString,&value) == .success,let value else{return nil}
    var pid:pid_t=0
    return AXUIElementGetPid(value as! AXUIElement,&pid) == .success ? pid : nil
}

private func frontmostPID() -> pid_t? { xmeshFrontmostPID() }

private func withTargetFocus(_ action:Action,_ operation:() throws -> Void) throws {
    let spi=SkyLightSPI.shared
    let focus=try spi.beginSyntheticTargetFocus(targetPID:action.pid,targetWindowID:action.window)
    do {
        try spi.makeSyntheticTargetWindowKey(focus)
        try operation()
    } catch {try? spi.endSyntheticTargetFocus(focus);throw error}
    try spi.endSyntheticTargetFocus(focus)
}

// Adapted from minghinmatthewlam/computer-use-mcp Input.swift at
// 82dd8093e44819c51537f489059e172777d7f1d7 (MIT, Copyright 2026 Matthew Lam).
// Changes: synchronous lease guard, fixed window affinity, private SkyLight
// routing and finite xmesh action budget. No process/global fallback is selected.
private func dispatchPointer(_ action:Action) throws {
    let start=try action.point(action.x,action.y)
    guard let source=CGEventSource(stateID:.privateState) else{throw ComputerUseError.message("event_source")}
    source.localEventsSuppressionInterval=0
    func post(_ type:CGEventType,_ point:CGPoint,_ cleanup:Bool=false) throws {
        let wasReleasing=releasingInput
        if cleanup{releasingInput=true}
        defer{releasingInput=wasReleasing}
        if !cleanup {try requireUsableComputerUseSession()}
        guard let event=CGEvent(mouseEventSource:source,mouseType:type,mouseCursorPosition:point,mouseButton:.left) else{throw ComputerUseError.message("event")}
        try stamp(event,point,action)
        try SkyLightSPI.shared.postToPid(event,pid:action.pid)
        event.postToPid(action.pid)
    }
    if action.type=="scroll"{
        let dx=action.deltaX ?? 0,dy=action.deltaY ?? 0
        guard abs(dx)<=10000,abs(dy)<=10000 else{throw ComputerUseError.invalidArguments("scroll")}
        try requireUsableComputerUseSession()
        guard let event=CGEvent(scrollWheelEvent2Source:source,units:.line,wheelCount:2,wheel1:Int32(-dy),wheel2:Int32(-dx),wheel3:0) else{throw ComputerUseError.message("event")}
        event.location=start
        try stamp(event,start,action)
        try SkyLightSPI.shared.postToPid(event,pid:action.pid)
        return
    }
    guard (action.button ?? "left")=="left" else{throw ComputerUseError.invalidArguments("button")}
    let end=try action.point(action.toX,action.toY)
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
        try stamp(event,point,action)
        try SkyLightSPI.shared.postToPid(event,pid:action.pid)
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
    let windows=CGWindowListCopyWindowInfo([.optionAll],kCGNullWindowID) as? [[String:Any]] ?? []
    guard skyKeyWindowMatchesTarget(windowInfo:windows,windowID:action.window,pid:action.pid) else{return}
    let previous=releasingInput;releasingInput=true
    defer{releasingInput=previous}
    guard let source=CGEventSource(stateID:.privateState),
          let event=CGEvent(mouseEventSource:source,mouseType:.leftMouseUp,mouseCursorPosition:point,mouseButton:.left) else{return}
    try stamp(event,point,action)
    try SkyLightSPI.shared.postToPid(event,pid:action.pid)
    event.postToPid(action.pid)
}
