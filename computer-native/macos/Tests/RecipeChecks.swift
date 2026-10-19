// Pure recipe and bridge-admission checks. No event is posted, no app is opened
// and no desktop screenshot is taken by this test executable.
import Foundation
import CoreGraphics

@main struct RecipeChecks {
    static func main() throws {
        let single=try skyClickEventRecipe(clickCount:1)
        precondition(single.count==5 && single.last?.kind == .up)
        let double=try skyClickEventRecipe(clickCount:2)
        precondition(double.count==7 && double.last?.clickState==2)
        do{_ = try skyClickEventRecipe(clickCount:3);preconditionFailure("unsupported click recipe accepted")}catch{}
        let records=skyLightKeyWindowRecords(windowID:0x12345678)
        precondition(records.count==2 && records[0][0x08]==1 && records[1][0x08]==2)
        precondition(Array(records[0][0x3C...0x3F])==[0x78,0x56,0x34,0x12])
        let plan=skyLightSyntheticTargetFocusPlan(targetPSN:[1,2,3,4,5,6,7,8],targetWindowID:4)
        precondition(plan.activateTarget.windowID==4 && plan.activateTarget.focused && !plan.deactivateTarget.focused)
        let info:[[String:Any]]=[[kCGWindowNumber as String:NSNumber(value:4),kCGWindowOwnerPID as String:NSNumber(value:42)]]
        precondition(skyKeyWindowMatchesTarget(windowInfo:info,windowID:4,pid:42))
        precondition(!skyKeyWindowMatchesTarget(windowInfo:info,windowID:4,pid:43))
        let keys=try KeyPressParser.parse("Command+Shift+A")
        precondition(keys.keyCode==0 && keys.modifiers.count==2)
        precondition(skyMenuKeyEquivalent(for:keys)?.modifiers==1)
        let chunks=InputSimulation.keyboardUnicodeChunks(for:"中🙂文",maxUTF16Units:2)
        precondition(chunks.map(\.count)==[1,2,1])
        precondition(chunks.flatMap{$0}==Array("中🙂文".utf16))
        precondition(xmeshBackgroundDispatch(nil,0,nil,nil)==1)
        let request=Data("{\"type\":\"click\",\"pid\":42,\"window\":4,\"originX\":0,\"originY\":0,\"width\":100,\"height\":80,\"x\":10,\"y\":10}".utf8)
        let cancelled=request.withUnsafeBytes{buffer in xmeshBackgroundDispatch(buffer.bindMemory(to:UInt8.self).baseAddress,request.count,{_ in false},nil)}
        precondition(cancelled==2)
        print("PASS: upstream click/focus/window-owner/key/Unicode recipes and pre-dispatch cancellation; no native input emitted")
    }
}
