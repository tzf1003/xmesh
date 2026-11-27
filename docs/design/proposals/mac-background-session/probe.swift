import AppKit
import ApplicationServices
import ScreenCaptureKit

setvbuf(stdout, nil, _IONBF, 0)
let dir = "/tmp/xmesh-bgprobe"
let statusPath = dir + "/target.status"
let args = Array(CommandLine.arguments.dropFirst())
let mode = args.first ?? "target"
let tsFmt: DateFormatter = { let f = DateFormatter(); f.dateFormat = "HH:mm:ss.SSS"; return f }()
func out(_ s: String) { print(tsFmt.string(from: Date()) + " " + s) }
func logLine(_ s: String) {
    let line = tsFmt.string(from: Date()) + " " + s + "\n"
    let path = dir + "/target.log"
    if let h = FileHandle(forWritingAtPath: path) { h.seekToEndOfFile(); h.write(line.data(using: .utf8)!); h.closeFile() }
    else { try? line.write(toFile: path, atomically: true, encoding: .utf8) }
}
func sessionFlags() -> String {
    guard let d = CGSessionCopyCurrentDictionary() as? [String: Any] else { return "session dictionary nil" }
    return "onConsole=\(d["kCGSSessionOnConsoleKey"] ?? "nil") loginDone=\(d["kCGSessionLoginDoneKey"] ?? "nil") locked=\(d["CGSSessionScreenIsLocked"] ?? "absent") user=\(d["kCGSSessionUserNameKey"] ?? "nil")"
}

var stage = "init"
var tapSeen: [String: Int] = [:]
let markerSession: Int64 = 0x70726F01
let markerHID: Int64 = 0x70726F02
let markerFlags: Int64 = 0x70726F03

// ---------------- target: a throwaway window owned by this probe ----------------
final class ProbeView: NSView {
    var tick = 0
    var draws = 0
    override var isFlipped: Bool { true }
    override func draw(_ dirtyRect: NSRect) {
        draws += 1
        NSColor(srgbRed: 1, green: 0, blue: 1, alpha: 1).setFill()
        NSRect(x: 0, y: 0, width: bounds.width, height: 60).fill()
        NSColor(calibratedHue: CGFloat(tick % 50) / 50.0, saturation: 0.7, brightness: 0.95, alpha: 1).setFill()
        NSRect(x: 0, y: 60, width: bounds.width, height: bounds.height - 60).fill()
        let attrs: [NSAttributedString.Key: Any] = [.font: NSFont.monospacedDigitSystemFont(ofSize: 36, weight: .bold), .foregroundColor: NSColor.black]
        ("tick \(tick)" as NSString).draw(at: NSPoint(x: 16, y: 80), withAttributes: attrs)
    }
}
final class Catcher: NSView {
    var scrolls = 0
    override func scrollWheel(with event: NSEvent) { scrolls += 1 }
    override func draw(_ dirtyRect: NSRect) { NSColor.darkGray.setFill(); bounds.fill() }
}
final class TargetDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var catcherWindow: NSWindow?
    let view = ProbeView(frame: NSRect(x: 0, y: 0, width: 420, height: 260))
    let catcher = Catcher(frame: NSRect(x: 0, y: 0, width: 120, height: 60))
    var button: NSButton!
    var field: NSTextField!
    var presses = 0
    var counts: [String: Int] = [:]
    var linkTicks = 0
    var link: CADisplayLink?
    var seconds: Double = 30
    var activate = false
    @objc func pressed(_ s: Any?) { presses += 1; logLine("button pressed #\(presses)") }
    @objc func linkFired(_ l: CADisplayLink) { linkTicks += 1 }
    func status() -> [String: Any] {
        let primaryH = NSScreen.screens.first?.frame.height ?? 0
        let f = window.frame
        let br = window.convertToScreen(button.convert(button.bounds, to: nil))
        var d: [String: Any] = [
            "pid": Int(getpid()), "windowNumber": window.windowNumber,
            "x": Double(f.minX), "y": Double(primaryH - f.maxY), "w": Double(f.width), "h": Double(f.height),
            "scale": Double(window.backingScaleFactor),
            "buttonX": Double(br.midX), "buttonY": Double(primaryH - br.midY),
            "tick": view.tick, "draws": view.draws, "linkTicks": linkTicks, "presses": presses,
            "field": field.stringValue, "counts": counts,
            "occlusionVisible": window.occlusionState.contains(.visible),
            "isVisible": window.isVisible, "isKey": window.isKeyWindow, "appActive": NSApp.isActive,
            "onActiveSpace": window.isOnActiveSpace, "catcherScrolls": catcher.scrolls,
            "cursorX": Double(CGEvent(source: nil)?.location.x ?? -1), "cursorY": Double(CGEvent(source: nil)?.location.y ?? -1),
            "fieldEditing": window.firstResponder is NSTextView,
        ]
        if let c = catcherWindow { d["catcherWindowNumber"] = c.windowNumber; d["catcherVisible"] = c.occlusionState.contains(.visible) }
        return d
    }
    func writeStatus() {
        if let data = try? JSONSerialization.data(withJSONObject: status(), options: [.sortedKeys]) {
            try? data.write(to: URL(fileURLWithPath: statusPath), options: .atomic)
        }
    }
    func applicationDidFinishLaunching(_ n: Notification) {
        window = NSWindow(contentRect: NSRect(x: 160, y: 300, width: 420, height: 260), styleMask: [.titled], backing: .buffered, defer: false)
        window.title = "xmesh-bgprobe"
        window.isReleasedWhenClosed = false
        window.contentView = view
        button = NSButton(title: "Press", target: self, action: #selector(pressed(_:)))
        button.frame = NSRect(x: 20, y: 200, width: 120, height: 36)
        view.addSubview(button)
        field = NSTextField(string: "")
        field.frame = NSRect(x: 160, y: 204, width: 240, height: 28)
        view.addSubview(field)
        window.orderFrontRegardless()
        let nc = NotificationCenter.default
        logLine("launch occlusionVisible=\(window.occlusionState.contains(.visible)) appActive=\(NSApp.isActive) isKey=\(window.isKeyWindow) " + sessionFlags())
        nc.addObserver(forName: NSWindow.didChangeOcclusionStateNotification, object: window, queue: .main) { [unowned self] _ in logLine("window occlusion -> visible=\(self.window.occlusionState.contains(.visible))") }
        nc.addObserver(forName: NSApplication.didChangeOcclusionStateNotification, object: nil, queue: .main) { _ in logLine("app occlusion -> visible=\(NSApp.occlusionState.contains(.visible))") }
        nc.addObserver(forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main) { _ in logLine("app didBecomeActive") }
        nc.addObserver(forName: NSApplication.didResignActiveNotification, object: nil, queue: .main) { _ in logLine("app didResignActive") }
        nc.addObserver(forName: NSWindow.didBecomeKeyNotification, object: window, queue: .main) { _ in logLine("window didBecomeKey") }
        nc.addObserver(forName: NSWindow.didResignKeyNotification, object: window, queue: .main) { _ in logLine("window didResignKey") }
        if let ev = CGEvent(source: nil) {
            let p = ev.location
            let primaryH = NSScreen.screens.first?.frame.height ?? 0
            let rect = NSRect(x: p.x - 60, y: primaryH - p.y - 30, width: 120, height: 60)
            let c = NSWindow(contentRect: rect, styleMask: [.borderless], backing: .buffered, defer: false)
            c.level = .screenSaver
            c.isReleasedWhenClosed = false
            c.contentView = catcher
            c.orderFrontRegardless()
            catcherWindow = c
        }
        NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .leftMouseUp, .keyDown, .keyUp, .scrollWheel, .flagsChanged, .mouseMoved]) { [unowned self] e in
            self.counts["type\(e.type.rawValue)", default: 0] += 1
            if e.type == .keyDown || e.type == .leftMouseDown { logLine("local event type\(e.type.rawValue)") }
            return e
        }
        if activate {
            NSApp.activate(ignoringOtherApps: true)
            window.makeKeyAndOrderFront(nil)
            window.makeFirstResponder(field)
        }
        link = view.displayLink(target: self, selector: #selector(linkFired(_:)))
        link?.add(to: .main, forMode: .common)
        Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [unowned self] _ in self.view.tick += 1; self.view.needsDisplay = true }
        Timer.scheduledTimer(withTimeInterval: 0.2, repeats: true) { [unowned self] _ in self.writeStatus() }
        Timer.scheduledTimer(withTimeInterval: seconds, repeats: false) { [unowned self] _ in
            self.writeStatus()
            NSApp.terminate(nil)
        }
    }
}

// ---------------- driver ----------------
func readStatus() -> [String: Any]? {
    guard let data = try? Data(contentsOf: URL(fileURLWithPath: statusPath)) else { return nil }
    return (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
}
func compact(_ st: [String: Any]) -> String {
    let keys = ["tick", "draws", "linkTicks", "presses", "field", "counts", "catcherScrolls", "occlusionVisible", "isVisible", "isKey", "appActive", "onActiveSpace", "catcherVisible", "cursorX", "cursorY", "fieldEditing"]
    return keys.map { "\($0)=\(st[$0] ?? "?")" }.joined(separator: " ").replacingOccurrences(of: "\n", with: "")
}
struct Stats { var w = 0; var h = 0; var r = 0.0; var g = 0.0; var b = 0.0; var blackFrac = 0.0; var magenta = 0; var distinct = 0; var hash: UInt64 = 0 }
func stats(_ img: CGImage) -> Stats {
    let w = img.width, h = img.height
    var buf = [UInt8](repeating: 0, count: w * h * 4)
    let cs = CGColorSpace(name: CGColorSpace.sRGB)!
    buf.withUnsafeMutableBytes { p in
        if let ctx = CGContext(data: p.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4, space: cs, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) {
            ctx.draw(img, in: CGRect(x: 0, y: 0, width: w, height: h))
        }
    }
    var s = Stats(); s.w = w; s.h = h
    var r = 0.0, g = 0.0, b = 0.0
    var black = 0, mag = 0
    var hash: UInt64 = 1469598103934665603
    var seen = Set<UInt32>()
    let n = w * h
    var i = 0
    while i < n {
        let o = i * 4
        let R = buf[o], G = buf[o + 1], B = buf[o + 2]
        r += Double(R); g += Double(G); b += Double(B)
        if R < 8 && G < 8 && B < 8 { black += 1 }
        if R > 215 && G < 60 && B > 215 { mag += 1 }
        hash = (hash ^ UInt64(R)) &* 1099511628211
        hash = (hash ^ UInt64(G)) &* 1099511628211
        hash = (hash ^ UInt64(B)) &* 1099511628211
        if i % 97 == 0 { seen.insert(UInt32(R) << 16 | UInt32(G) << 8 | UInt32(B)) }
        i += 1
    }
    if n > 0 { s.r = r / Double(n); s.g = g / Double(n); s.b = b / Double(n); s.blackFrac = Double(black) / Double(n) }
    s.magenta = mag; s.distinct = seen.count; s.hash = hash
    return s
}
func describe(_ s: Stats) -> String {
    String(format: "%dx%d mean=(%.1f,%.1f,%.1f) black=%.3f magentaPx=%d distinctSampled=%d hash=%016llx", s.w, s.h, s.r, s.g, s.b, s.blackFrac, s.magenta, s.distinct, s.hash)
}
func savePNG(_ img: CGImage, _ path: String) {
    let rep = NSBitmapImageRep(cgImage: img)
    if let d = rep.representation(using: .png, properties: [:]) { try? d.write(to: URL(fileURLWithPath: path)) }
}
func axString(_ el: AXUIElement, _ name: String) -> String {
    var v: CFTypeRef?
    return AXUIElementCopyAttributeValue(el, name as CFString, &v) == .success ? (v as? String ?? "") : ""
}
func axChildren(_ el: AXUIElement) -> [AXUIElement] {
    var v: CFTypeRef?
    return AXUIElementCopyAttributeValue(el, kAXChildrenAttribute as CFString, &v) == .success ? (v as? [AXUIElement] ?? []) : []
}
let tapCallback: CGEventTapCallBack = { _, type, event, _ in
    let m = event.getIntegerValueField(.eventSourceUserData)
    tapSeen["type\(type.rawValue)_marker\(String(m, radix: 16))", default: 0] += 1
    return Unmanaged.passUnretained(event)
}
func postScroll(_ loc: CGEventTapLocation, _ marker: Int64) {
    if let ev = CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 1, wheel1: 1, wheel2: 0, wheel3: 0) {
        ev.setIntegerValueField(.eventSourceUserData, value: marker)
        ev.post(tap: loc)
    }
}

@MainActor func drive(_ stages: [String]) async {
    let st0 = readStatus() ?? [:]
    let pid = pid_t((st0["pid"] as? Int) ?? 0), wid = CGWindowID((st0["windowNumber"] as? Int) ?? 0)
    out("session.before " + sessionFlags())
    out("target.before " + compact(st0))
    for s in stages {
        stage = s
        switch s {
        case "ax":
            let appEl = AXUIElementCreateApplication(pid)
            AXUIElementSetMessagingTimeout(appEl, 2.0)
            var v: CFTypeRef?
            let e = AXUIElementCopyAttributeValue(appEl, kAXWindowsAttribute as CFString, &v)
            let wins = (v as? [AXUIElement]) ?? []
            out("ax.windows err=\(e.rawValue) count=\(wins.count)")
            var queue = wins.map { ($0, 0) }
            var btn: AXUIElement?
            var tf: AXUIElement?
            var visited = 0
            while !queue.isEmpty, visited < 200 {
                let (el, depth) = queue.removeFirst()
                visited += 1
                let role = axString(el, kAXRoleAttribute)
                if role == (kAXButtonRole as String), axString(el, kAXTitleAttribute) == "Press" { btn = el }
                if role == (kAXTextFieldRole as String) { tf = el }
                if depth < 5 { queue.append(contentsOf: axChildren(el).map { ($0, depth + 1) }) }
            }
            out("ax.tree visited=\(visited) button=\(btn != nil) textField=\(tf != nil)")
            if let btn { out("ax.press err=\(AXUIElementPerformAction(btn, kAXPressAction as CFString).rawValue)") }
            if let tf {
                out("ax.setValue err=\(AXUIElementSetAttributeValue(tf, kAXValueAttribute as CFString, "ax-ok" as CFString).rawValue)")
                out("ax.readBack=\(axString(tf, kAXValueAttribute))")
            }
        case "pidevents":
            let bx = (st0["buttonX"] as? Double) ?? 0, by = (st0["buttonY"] as? Double) ?? 0
            let src = CGEventSource(stateID: .privateState)
            for t in [CGEventType.leftMouseDown, .leftMouseUp] {
                if let ev = CGEvent(mouseEventSource: src, mouseType: t, mouseCursorPosition: CGPoint(x: bx, y: by), mouseButton: .left) {
                    ev.setIntegerValueField(.mouseEventClickState, value: 1)
                    ev.postToPid(pid)
                }
                usleep(40_000)
            }
            for down in [true, false] {
                if let ev = CGEvent(keyboardEventSource: src, virtualKey: 0, keyDown: down) { ev.postToPid(pid) }
                usleep(30_000)
            }
            out("pidevents posted mouse down/up at (\(bx),\(by)) and key a down/up")
        case "tapevents":
            let mask: CGEventMask = (CGEventMask(1) << CGEventMask(CGEventType.scrollWheel.rawValue)) | (CGEventMask(1) << CGEventMask(CGEventType.flagsChanged.rawValue))
            if let tap = CGEvent.tapCreate(tap: .cgSessionEventTap, place: .headInsertEventTap, options: .listenOnly, eventsOfInterest: mask, callback: tapCallback, userInfo: nil) {
                let src = CFMachPortCreateRunLoopSource(nil, tap, 0)
                CFRunLoopAddSource(CFRunLoopGetMain(), src, .commonModes)
                CGEvent.tapEnable(tap: tap, enable: true)
                out("tap.created=true")
            } else { out("tap.created=false") }
            try? await Task.sleep(nanoseconds: 200_000_000)
            postScroll(.cgSessionEventTap, markerSession)
            try? await Task.sleep(nanoseconds: 400_000_000)
            out("tap.afterSessionScroll seen=\(tapSeen) catcherScrolls=\(readStatus()?["catcherScrolls"] ?? "?")")
            postScroll(.cghidEventTap, markerHID)
            try? await Task.sleep(nanoseconds: 400_000_000)
            out("tap.afterHIDScroll seen=\(tapSeen) catcherScrolls=\(readStatus()?["catcherScrolls"] ?? "?")")
            if let ev = CGEvent(source: nil) {
                ev.type = .flagsChanged
                ev.flags = []
                ev.setIntegerValueField(.eventSourceUserData, value: markerFlags)
                ev.post(tap: .cgSessionEventTap)
            }
            try? await Task.sleep(nanoseconds: 300_000_000)
            out("tap.afterFlags seen=\(tapSeen)")
        case "sckwin":
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
                out("sck.content displays=\(content.displays.count) windows=\(content.windows.count) apps=\(content.applications.count)")
                for d in content.displays { out("sck.display id=\(d.displayID) size=\(d.width)x\(d.height)") }
                guard let w = content.windows.first(where: { $0.windowID == wid }) else { out("sck.targetWindow NOT FOUND"); break }
                out("sck.targetWindow onScreen=\(w.isOnScreen) active=\(w.isActive) layer=\(w.windowLayer) frame=\(w.frame)")
                let filter = SCContentFilter(desktopIndependentWindow: w)
                let scale = CGFloat(filter.pointPixelScale)
                let cfg = SCStreamConfiguration()
                cfg.width = Int(w.frame.width * scale)
                cfg.height = Int(w.frame.height * scale)
                cfg.showsCursor = false
                var last: Stats?
                for i in 1...3 {
                    let t0 = Date()
                    let img = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: cfg)
                    let st = stats(img)
                    let changed = last.map { $0.hash != st.hash } ?? false
                    out(String(format: "capture.window#%d %.0fms ", i, Date().timeIntervalSince(t0) * 1000) + describe(st) + " changedSincePrev=\(changed)")
                    last = st
                    if i == 3 { savePNG(img, dir + "/window.png") }
                    try await Task.sleep(nanoseconds: 600_000_000)
                }
            } catch { out("sckwin.error \(error)") }
        case "sckdisp":
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
                let main = CGMainDisplayID()
                guard let d = content.displays.first(where: { $0.displayID == main }) ?? content.displays.first else { out("sck.noDisplay"); break }
                let filter = SCContentFilter(display: d, excludingWindows: [])
                let cfg = SCStreamConfiguration()
                cfg.width = d.width
                cfg.height = d.height
                cfg.showsCursor = true
                var last: Stats?
                for i in 1...2 {
                    let t0 = Date()
                    let img = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: cfg)
                    let st = stats(img)
                    let changed = last.map { $0.hash != st.hash } ?? false
                    out(String(format: "capture.display#%d %.0fms ", i, Date().timeIntervalSince(t0) * 1000) + "id=\(d.displayID) " + describe(st) + " changedSincePrev=\(changed)")
                    last = st
                    if i == 2 { savePNG(img, dir + "/display.png") }
                    try await Task.sleep(nanoseconds: 600_000_000)
                }
                out("capture.display expectedMagentaPx~\(420 * 60) at 1x if the probe window is composited")
            } catch { out("sckdisp.error \(error)") }
        case "hidcheck":
            func idle(_ st: CGEventSourceStateID, _ t: CGEventType) -> Double { CGEventSource.secondsSinceLastEventType(st, eventType: t) }
            for round in 1...2 {
                let b1 = idle(.hidSystemState, .flagsChanged), b2 = idle(.combinedSessionState, .flagsChanged)
                if let ev = CGEvent(source: nil) {
                    ev.type = .flagsChanged
                    ev.flags = []
                    ev.setIntegerValueField(.eventSourceUserData, value: markerHID)
                    ev.post(tap: .cghidEventTap)
                }
                usleep(60_000)
                out(String(format: "hidcheck.flags#%d post@HID: hidState %.3f -> %.3f | sessionState %.3f -> %.3f", round, b1, idle(.hidSystemState, .flagsChanged), b2, idle(.combinedSessionState, .flagsChanged)))
                try? await Task.sleep(nanoseconds: 300_000_000)
            }
            let c1 = idle(.hidSystemState, .flagsChanged), c2 = idle(.combinedSessionState, .flagsChanged)
            if let ev = CGEvent(source: nil) {
                ev.type = .flagsChanged
                ev.flags = []
                ev.setIntegerValueField(.eventSourceUserData, value: markerFlags)
                ev.post(tap: .cgSessionEventTap)
            }
            usleep(60_000)
            out(String(format: "hidcheck.flags control post@Session: hidState %.3f -> %.3f | sessionState %.3f -> %.3f", c1, idle(.hidSystemState, .flagsChanged), c2, idle(.combinedSessionState, .flagsChanged)))
            let s1 = idle(.hidSystemState, .scrollWheel), s2 = idle(.combinedSessionState, .scrollWheel)
            postScroll(.cghidEventTap, markerHID)
            usleep(60_000)
            out(String(format: "hidcheck.scroll post@HID: hidState %.3f -> %.3f | sessionState %.3f -> %.3f", s1, idle(.hidSystemState, .scrollWheel), s2, idle(.combinedSessionState, .scrollWheel)))
        case "sessionclick", "sessionclickfield":
            let bx = (st0["buttonX"] as? Double) ?? 0, by = (st0["buttonY"] as? Double) ?? 0
            let before = CGEvent(source: nil)?.location ?? .zero
            let src = CGEventSource(stateID: .combinedSessionState)
            func mouse(_ t: CGEventType, _ p: CGPoint) {
                if let ev = CGEvent(mouseEventSource: src, mouseType: t, mouseCursorPosition: p, mouseButton: .left) {
                    ev.setIntegerValueField(.eventSourceUserData, value: markerSession)
                    if t != .mouseMoved { ev.setIntegerValueField(.mouseEventClickState, value: 1) }
                    ev.post(tap: .cgSessionEventTap)
                }
            }
            let p = CGPoint(x: bx + (s == "sessionclickfield" ? 200.0 : 0.0), y: by)
            mouse(.mouseMoved, p)
            usleep(80_000)
            let moved = CGEvent(source: nil)?.location ?? .zero
            mouse(.leftMouseDown, p)
            usleep(40_000)
            mouse(.leftMouseUp, p)
            usleep(200_000)
            out("sessionclick cursor before=\(before) afterMove=\(moved) presses=\(readStatus()?["presses"] ?? "?")")
            mouse(.mouseMoved, before)
            usleep(60_000)
            out("sessionclick cursor restored=\(CGEvent(source: nil)?.location ?? .zero)")
        case "sessionkey":
            let cur = readStatus() ?? [:]
            guard (cur["isKey"] as? Bool) == true, (cur["fieldEditing"] as? Bool) == true else { out("sessionkey SKIPPED, probe window is not key and editing: " + compact(cur)); break }
            let src = CGEventSource(stateID: .combinedSessionState)
            for down in [true, false] {
                if let ev = CGEvent(keyboardEventSource: src, virtualKey: 11, keyDown: down) {
                    ev.setIntegerValueField(.eventSourceUserData, value: markerSession)
                    ev.post(tap: .cgSessionEventTap)
                }
                usleep(40_000)
            }
            let chars = Array("中文".utf16)
            for down in [true, false] {
                if let ev = CGEvent(keyboardEventSource: src, virtualKey: 0, keyDown: down) {
                    ev.keyboardSetUnicodeString(stringLength: chars.count, unicodeString: chars)
                    ev.setIntegerValueField(.eventSourceUserData, value: markerSession)
                    ev.post(tap: .cgSessionEventTap)
                }
                usleep(40_000)
            }
            out("sessionkey posted key b and unicode text at the session tap")
        case "realwindows":
            do {
                let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
                let wins = content.windows.filter { $0.windowLayer == 0 && $0.windowID != wid && $0.frame.width >= 200 && $0.frame.height >= 150 }
                out("realwindows candidates=\(wins.count)")
                for w in wins.prefix(6) {
                    let name = w.owningApplication?.applicationName ?? "?"
                    let filter = SCContentFilter(desktopIndependentWindow: w)
                    let cfg = SCStreamConfiguration()
                    cfg.width = Int(w.frame.width)
                    cfg.height = Int(w.frame.height)
                    cfg.showsCursor = false
                    do {
                        let t0 = Date()
                        let img = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: cfg)
                        out(String(format: "realwindow %.0fms ", Date().timeIntervalSince(t0) * 1000) + "owner=\(name) " + describe(stats(img)))
                    } catch { out("realwindow owner=\(name) error=\(error)") }
                }
            } catch { out("realwindows.error \(error)") }
        case "watch":
            guard let ps = ProcessInfo.processInfo.environment["WATCH_PID"], let wp = Int32(ps) else { out("watch: WATCH_PID missing"); break }
            for round in 1...4 {
                do {
                    let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: false)
                    let wins = content.windows.filter { $0.owningApplication?.processID == wp && $0.windowLayer == 0 && $0.frame.width >= 200 && $0.frame.height >= 150 }
                    if wins.isEmpty { out("watch#\(round) no windows for pid \(wp)") }
                    for w in wins.prefix(2) {
                        let filter = SCContentFilter(desktopIndependentWindow: w)
                        let cfg = SCStreamConfiguration()
                        cfg.width = Int(w.frame.width)
                        cfg.height = Int(w.frame.height)
                        cfg.showsCursor = false
                        let img = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: cfg)
                        out("watch#\(round) onScreen=\(w.isOnScreen) active=\(w.isActive) title=\(w.title ?? "nil") | " + describe(stats(img)))
                    }
                } catch { out("watch#\(round) error \(error)") }
                try? await Task.sleep(nanoseconds: 1_500_000_000)
            }
        default:
            out("unknown stage \(s)")
        }
        try? await Task.sleep(nanoseconds: 400_000_000)
        if let st = readStatus() { out("target.after[\(s)] " + compact(st)) }
    }
    out("session.after " + sessionFlags())
}

if mode == "target" {
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    let d = TargetDelegate()
    if args.count > 1, let s = Double(args[1]) { d.seconds = s }
    d.activate = args.contains("activate")
    app.delegate = d
    app.run()
} else {
    let stages = Array(args.dropFirst())
    NSApplication.shared.setActivationPolicy(.prohibited)
    _ = CGMainDisplayID()
    DispatchQueue.global().asyncAfter(deadline: .now() + 45) { out("driver: WATCHDOG at stage \(stage)"); exit(3) }
    Task { @MainActor in
        await drive(stages)
        exit(0)
    }
    RunLoop.main.run()
}
