// Adapted from minghinmatthewlam/computer-use-mcp Core/HitTest.swift,
// Core/AX.swift, Tools/ClickTool.swift and Tools/TextTools.swift at
// 82dd8093e44819c51537f489059e172777d7f1d7, MIT (Copyright 2026 Matthew Lam).
// Changes: synchronous, bounded, exact-window validation; once AX dispatch is
// attempted an error is propagated rather than followed by another click.
import ApplicationServices
import CoreGraphics
import Foundation

private func attribute(_ element:AXUIElement,_ name:String)->CFTypeRef? {
    var value:CFTypeRef?
    return AXUIElementCopyAttributeValue(element,name as CFString,&value) == .success ? value : nil
}

private func elementAttribute(_ element:AXUIElement,_ name:String)->AXUIElement? {
    guard let value=attribute(element,name),CFGetTypeID(value)==AXUIElementGetTypeID() else{return nil}
    return unsafeBitCast(value,to:AXUIElement.self)
}

private func actionNames(_ element:AXUIElement)->[String] {
    var names:CFArray?
    guard AXUIElementCopyActionNames(element,&names) == .success else{return []}
    return names as? [String] ?? []
}

private func ownedByWindow(_ element:AXUIElement,pid:pid_t,window:CGWindowID)->Bool{
    var owner:pid_t=0
    guard AXUIElementGetPid(element,&owner) == .success,owner==pid else{return false}
    let windowElement=elementAttribute(element,kAXWindowAttribute)
        ?? ((attribute(element,kAXRoleAttribute) as? String)==kAXWindowRole as String ? element : nil)
    guard let windowElement else{return false}
    return SkyLightSPI.shared.windowID(for:windowElement)==window
}

/// Returns false only when no semantic route was selected before dispatch.
/// True means AX accepted the mutation; the caller still observes business state.
func xmeshAccessibilityClick(pid:pid_t,window:CGWindowID,point:CGPoint,button:String,count:Int)throws->Bool{
    guard count==1,button=="left" || button=="right" else{return false}
    let app=AXUIElementCreateApplication(pid)
    AXUIElementSetMessagingTimeout(app,0.25)
    var hit:AXUIElement?
    guard AXUIElementCopyElementAtPosition(app,Float(point.x),Float(point.y),&hit) == .success,
          var current=hit else{return false}
    let action=button=="left" ? kAXPressAction as String : "AXShowMenu"
    for _ in 0...5{
        try requireUsableComputerUseSession()
        guard ownedByWindow(current,pid:pid,window:window) else{return false}
        if actionNames(current).contains(action){
            try requireUsableComputerUseSession()
            let result=AXUIElementPerformAction(current,action as CFString)
            guard result == .success else{throw ComputerUseError.message("accessibility_action_failed")}
            return true
        }
        // TextTools' focus-before-type route: focus an editable target inside
        // the app, then prove that the app's focused element is this object.
        let role=attribute(current,kAXRoleAttribute) as? String
        if button=="left",[kAXTextFieldRole as String,kAXTextAreaRole as String].contains(role ?? ""){
            // A coordinate click also chooses a caret. Focus alone is exact
            // only for an empty field; populated fields need AX point mapping.
            guard let text=attribute(current,kAXValueAttribute) as? String else{return false}
            var caret:AXValue?
            if !text.isEmpty{
                var selectionSettable=DarwinBoolean(false)
                var position=point
                var result:CFTypeRef?
                guard AXUIElementIsAttributeSettable(current,kAXSelectedTextRangeAttribute as CFString,&selectionSettable) == .success,selectionSettable.boolValue,
                      let positionValue=AXValueCreate(.cgPoint,&position),
                      AXUIElementCopyParameterizedAttributeValue(current,kAXRangeForPositionParameterizedAttribute as CFString,positionValue,&result) == .success,
                      let result,CFGetTypeID(result)==AXValueGetTypeID() else{return false}
                var range=CFRange(location:0,length:0)
                guard AXValueGetValue(unsafeBitCast(result,to:AXValue.self),.cfRange,&range) else{return false}
                range.length=0
                _ = try xmeshSpliceText(text,selection:range,text:"")
                caret=AXValueCreate(.cfRange,&range)
                guard caret != nil else{return false}
            }
            var settable=DarwinBoolean(false)
            if AXUIElementIsAttributeSettable(current,kAXFocusedAttribute as CFString,&settable) == .success,settable.boolValue{
                try requireUsableComputerUseSession()
                guard AXUIElementSetAttributeValue(current,kAXFocusedAttribute as CFString,kCFBooleanTrue) == .success else{throw ComputerUseError.message("accessibility_focus_failed")}
                guard let focused=elementAttribute(app,kAXFocusedUIElementAttribute),CFEqual(focused,current) else{throw ComputerUseError.message("accessibility_focus_unverified")}
                if let caret{
                    try requireUsableComputerUseSession()
                    guard AXUIElementSetAttributeValue(current,kAXSelectedTextRangeAttribute as CFString,caret) == .success else{throw ComputerUseError.message("accessibility_caret_failed")}
                }
                return true
            }
        }
        guard let parent=elementAttribute(current,kAXParentAttribute) else{return false}
        current=parent
    }
    return false
}

// Adapted from the same upstream TextTools.insertText route. Selected text is
// replaced directly when the field supports it; otherwise splice its value at
// the exposed UTF-16 selection. Route selection happens before any mutation.
func xmeshSpliceText(_ value:String,selection:CFRange,text:String)throws->String{
    let current=value as NSString
    let maximum=1_048_576
    func validBoundary(_ offset:Int)->Bool{
        offset==0 || offset==current.length || !((0xD800...0xDBFF).contains(current.character(at:offset-1)) && (0xDC00...0xDFFF).contains(current.character(at:offset)))
    }
    guard current.length<=maximum,text.utf16.count<=65536,
          selection.location>=0,selection.length>=0,selection.location<=current.length,
          selection.length<=current.length-selection.location,
          current.length-selection.length<=maximum-text.utf16.count,
          validBoundary(selection.location),validBoundary(selection.location+selection.length)
    else{throw ComputerUseError.stateUnavailable("selection_invalid")}
    return current.replacingCharacters(in:NSRange(location:selection.location,length:selection.length),with:text)
}

func xmeshAccessibilityText(pid:pid_t,window:CGWindowID,text:String)throws->Bool{
    let app=AXUIElementCreateApplication(pid)
    AXUIElementSetMessagingTimeout(app,0.25)
    guard let element=elementAttribute(app,kAXFocusedUIElementAttribute),ownedByWindow(element,pid:pid,window:window) else{return false}
    let role=attribute(element,kAXRoleAttribute) as? String
    guard [kAXTextFieldRole as String,kAXTextAreaRole as String].contains(role ?? "") else{return false}
    func settable(_ name:String)->Bool{
        var value=DarwinBoolean(false)
        return AXUIElementIsAttributeSettable(element,name as CFString,&value) == .success && value.boolValue
    }
    let name:String
    let replacement:String
    if settable(kAXSelectedTextAttribute){name=kAXSelectedTextAttribute;replacement=text}
    else if settable(kAXValueAttribute),let value=attribute(element,kAXValueAttribute) as? String,
            let selection=attribute(element,kAXSelectedTextRangeAttribute),CFGetTypeID(selection)==AXValueGetTypeID(){
        var range=CFRange(location:0,length:0)
        guard AXValueGetValue(unsafeBitCast(selection,to:AXValue.self),.cfRange,&range) else{throw ComputerUseError.stateUnavailable("selection_invalid")}
        name=kAXValueAttribute
        replacement=try xmeshSpliceText(value,selection:range,text:text)
    }else{return false}
    try requireUsableComputerUseSession()
    guard ownedByWindow(element,pid:pid,window:window),
          let focused=elementAttribute(app,kAXFocusedUIElementAttribute),CFEqual(focused,element) else{throw ComputerUseError.stateUnavailable("focus_changed")}
    guard AXUIElementSetAttributeValue(element,name as CFString,replacement as CFString) == .success else{throw ComputerUseError.message("accessibility_text_failed")}
    return true
}

func xmeshAccessibilitySelectAll(pid:pid_t,window:CGWindowID,keys:[String])throws->Bool{
    guard keys.count==2,["meta","command","cmd"].contains(keys[0].lowercased()),keys[1].lowercased()=="a" else{return false}
    let app=AXUIElementCreateApplication(pid)
    AXUIElementSetMessagingTimeout(app,0.25)
    guard let element=elementAttribute(app,kAXFocusedUIElementAttribute),ownedByWindow(element,pid:pid,window:window),
          let role=attribute(element,kAXRoleAttribute) as? String,[kAXTextFieldRole as String,kAXTextAreaRole as String].contains(role),
          let text=attribute(element,kAXValueAttribute) as? String else{return false}
    var settable=DarwinBoolean(false)
    guard AXUIElementIsAttributeSettable(element,kAXSelectedTextRangeAttribute as CFString,&settable) == .success,settable.boolValue else{return false}
    guard (text as NSString).length<=1_048_576 else{throw ComputerUseError.stateUnavailable("selection_invalid")}
    var range=CFRange(location:0,length:(text as NSString).length)
    guard let value=AXValueCreate(.cfRange,&range) else{throw ComputerUseError.message("selection_value")}
    try requireUsableComputerUseSession()
    guard AXUIElementSetAttributeValue(element,kAXSelectedTextRangeAttribute as CFString,value) == .success else{throw ComputerUseError.message("selection_failed")}
    return true
}
