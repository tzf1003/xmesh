// Disposable AppKit target for opt-in background acceptance. It never activates
// itself, never changes the system cursor and only writes its own test state.
import AppKit
import CoreGraphics
import Foundation

final class DragProbe:NSView{
    var completed=false
    private var start:CGFloat?
    var events:[String]=[]
    override func draw(_ dirtyRect:NSRect){NSColor.systemGreen.setFill();bounds.fill()}
    override func acceptsFirstMouse(for event:NSEvent?)->Bool{true}
    override func mouseDown(with event:NSEvent){events.append("down");start=event.locationInWindow.x}
    override func mouseDragged(with event:NSEvent){events.append("drag")}
    override func mouseUp(with event:NSEvent){events.append("up");completed=(start.map{event.locationInWindow.x-$0>80} ?? false);start=nil}
}

final class BackgroundFixture:NSObject,NSApplicationDelegate{
    let directory:URL
    var window:NSWindow!
    var button:NSButton!
    var text:NSTextView!
    var drag:DragProbe!
    var scroll:NSScrollView!
    var count=0
    var timer:Timer?
    var occluder:NSWindow?
    var pointerEvents:[[String:Any]]=[]
    var monitor:Any?
    init(_ directory:URL){self.directory=directory}
    func applicationWillFinishLaunching(_ notification:Notification){NSApp.setActivationPolicy(.regular)}
    func applicationDidFinishLaunching(_ notification:Notification){
        let menu=NSMenu()
        let edit=NSMenuItem(title:"Edit",action:nil,keyEquivalent:"")
        let commands=NSMenu(title:"Edit")
        commands.addItem(withTitle:"Select All",action:#selector(NSText.selectAll(_:)),keyEquivalent:"a")
        edit.submenu=commands;menu.addItem(edit);NSApp.mainMenu=menu
        let screen=NSScreen.main?.visibleFrame ?? NSRect(x:0,y:0,width:1280,height:800)
        window=NSWindow(contentRect:NSRect(x:screen.minX+80,y:screen.maxY-600,width:600,height:480),styleMask:[.titled,.closable],backing:.buffered,defer:false)
        window.title="Xmesh background acceptance"
        let content=window.contentView!
        button=NSButton(title:"Background click",target:self,action:#selector(clicked))
        button.frame=NSRect(x:30,y:400,width:200,height:35)
        button.setAccessibilityIdentifier("xmesh-background-click")
        content.addSubview(button)
        text=NSTextView(frame:NSRect(x:30,y:290,width:520,height:80))
        text.isRichText=false;text.font=NSFont.systemFont(ofSize:16)
        text.setAccessibilityIdentifier("xmesh-background-text")
        content.addSubview(text)
        drag=DragProbe(frame:NSRect(x:30,y:160,width:520,height:95))
        drag.setAccessibilityIdentifier("xmesh-background-drag")
        content.addSubview(drag)
        scroll=NSScrollView(frame:NSRect(x:30,y:25,width:520,height:100))
        scroll.hasVerticalScroller=true
        let document=NSTextView(frame:NSRect(x:0,y:0,width:480,height:900))
        document.isEditable=false
        document.string=(0..<40).map{"Background scroll row \($0)"}.joined(separator:"\n")
        scroll.documentView=document
        scroll.setAccessibilityIdentifier("xmesh-background-scroll")
        content.addSubview(scroll)
        window.makeFirstResponder(button)
        window.orderBack(nil)
        if ProcessInfo.processInfo.environment["XMESH_BACKGROUND_FIXTURE_OCCLUDED"]=="1"{
            // A second window of this fixture covers the target, behind the
            // user's windows. Its controls must never receive target input.
            let cover=NSWindow(contentRect:window.frame,styleMask:[.borderless],backing:.buffered,defer:false)
            cover.title="Background acceptance cover"
            cover.backgroundColor=NSColor.windowBackgroundColor
            let label=NSTextField(labelWithString:"Covered background target")
            label.frame=NSRect(x:30,y:30,width:400,height:40)
            cover.contentView?.addSubview(label)
            cover.order(.above,relativeTo:window.windowNumber)
            occluder=cover
        }
        window.resignKey()
        NSApp.deactivate()
        monitor=NSEvent.addLocalMonitorForEvents(matching:[.leftMouseDown,.leftMouseDragged,.leftMouseUp,.scrollWheel]){[weak self]event in
            self?.pointerEvents.append(["type":event.type.rawValue,"x":event.locationInWindow.x,"y":event.locationInWindow.y,"window":event.windowNumber])
            if (self?.pointerEvents.count ?? 0)>100{self?.pointerEvents.removeFirst()}
            return event
        }
        timer=Timer.scheduledTimer(withTimeInterval:0.03,repeats:true){[weak self]_ in self?.publish()}
        publish()
    }
    @objc func clicked(){count+=1;publish()}
    func publish(){
        func point(_ view:NSView,_ dx:CGFloat=0)->[String:Double]{
            let rect=view.convert(view.bounds,to:nil)
            let screen=window.convertToScreen(rect)
            let reference=NSScreen.screens.first?.frame.maxY ?? 0
            let info=CGWindowListCopyWindowInfo(.optionIncludingWindow,CGWindowID(window.windowNumber)) as? [[String:Any]] ?? []
            let bounds=(info.first?[kCGWindowBounds as String] as? [String:Any]).flatMap{CGRect(dictionaryRepresentation:$0 as CFDictionary)} ?? .zero
            return ["x":screen.midX-bounds.minX+dx,"y":reference-screen.midY-bounds.minY]
        }
        let state:[String:Any]=["pid":ProcessInfo.processInfo.processIdentifier,"window":window.windowNumber,
            "clicks":count,"value":text.string,"drag_complete":drag.completed,"scroll_y":scroll.contentView.bounds.origin.y,
            "button":point(button),"text":point(text),"drag":point(drag,-100),"drag_to":point(drag,100),"scroll":point(scroll),"pointer_events":pointerEvents,
            "frontmost_pid":NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0,"drag_events":drag.events,
            "is_active":NSApp.isActive,"is_key":window.isKeyWindow,"text_focused":window.firstResponder===text,"occluded":occluder != nil]
        if let data=try? JSONSerialization.data(withJSONObject:state,options:.sortedKeys){try? data.write(to:directory.appendingPathComponent("state.json"),options:.atomic)}
    }
}

@main struct FixtureMain{
    static func main(){
        guard CommandLine.arguments.count==2 else{exit(2)}
        let directory=URL(fileURLWithPath:CommandLine.arguments[1],isDirectory:true)
        let app=NSApplication.shared
        app.setActivationPolicy(.regular)
        let delegate=BackgroundFixture(directory)
        app.delegate=delegate
        withExtendedLifetime(delegate){app.run()}
    }
}
