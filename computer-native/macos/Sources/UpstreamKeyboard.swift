// Adapted from iFurySt/open-computer-use InputSimulation.swift at 5136415f52b87f419227bae28767cf469a6bc3b5
// MIT; full license and transitive notices are in ../vendor/open-computer-use.
// Changes: retain process-directed text/chord methods, use private event
// sources, and release pressed modifiers when the xmesh guard cancels a chord.
import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

enum InputSimulation {
    static let maxKeyboardUnicodeChunkLength = 64
    static func typeText(_ text: String, pid: pid_t) throws {
        for chunk in keyboardUnicodeChunks(for: text) {
            try requireUsableComputerUseSession()
            var mutableChunk = chunk
            guard let down = CGEvent(keyboardEventSource: CGEventSource(stateID: .privateState), virtualKey: 0, keyDown: true),
                  let up = CGEvent(keyboardEventSource: CGEventSource(stateID: .privateState), virtualKey: 0, keyDown: false) else {
                throw ComputerUseError.message("Failed to create keyboard event.")
            }

            mutableChunk.withUnsafeMutableBufferPointer { buffer in
                guard let baseAddress = buffer.baseAddress else {
                    return
                }

                down.keyboardSetUnicodeString(stringLength: buffer.count, unicodeString: baseAddress)
                up.keyboardSetUnicodeString(stringLength: buffer.count, unicodeString: baseAddress)
            }
            down.postToPid(pid)
            xmeshReleaseInput {up.postToPid(pid)}
            if InputTiming.typeChunkDelay > 0 {
                Thread.sleep(forTimeInterval: InputTiming.typeChunkDelay)
            }
        }
    }

    static func keyboardUnicodeChunks(for text: String, maxUTF16Units: Int = maxKeyboardUnicodeChunkLength) -> [[UniChar]] {
        precondition(maxUTF16Units > 0, "maxUTF16Units must be positive")

        var chunks: [[UniChar]] = []
        var current: [UniChar] = []

        for character in text {
            let units = Array(String(character).utf16)
            if !current.isEmpty, current.count + units.count > maxUTF16Units {
                chunks.append(current)
                current.removeAll(keepingCapacity: true)
            }

            current.append(contentsOf: units)
        }

        if !current.isEmpty {
            chunks.append(current)
        }

        return chunks
    }

    static func pressKey(_ specification: String, pid: pid_t) throws {
        try requireUsableComputerUseSession()
        let parsed = try KeyPressParser.parse(specification)
        var activeFlags: CGEventFlags = []
        let source=CGEventSource(stateID:.privateState)
        var held:[ParsedKeyPress.Modifier]=[]
        defer{
            for modifier in held.reversed(){
                activeFlags.remove(modifier.flag)
                if let event=CGEvent(keyboardEventSource:source,virtualKey:modifier.keyCode,keyDown:false){
                    event.flags=activeFlags
                    xmeshReleaseInput {event.postToPid(pid)}
                }
            }
        }

        for modifier in parsed.modifiers {
            try requireUsableComputerUseSession()
            guard let event = CGEvent(keyboardEventSource: source, virtualKey: modifier.keyCode, keyDown: true) else {
                throw ComputerUseError.message("Failed to create modifier key down event.")
            }

            activeFlags.insert(modifier.flag)
            event.flags = activeFlags
            event.postToPid(pid)
            held.append(modifier)
        }

        try requireUsableComputerUseSession()
        guard let keyDown = CGEvent(keyboardEventSource: source, virtualKey: parsed.keyCode, keyDown: true),
              let keyUp = CGEvent(keyboardEventSource: source, virtualKey: parsed.keyCode, keyDown: false) else {
            throw ComputerUseError.message("Failed to create key event.")
        }

        keyDown.flags = activeFlags
        keyUp.flags = activeFlags
        keyDown.postToPid(pid)
        xmeshReleaseInput {keyUp.postToPid(pid)}

        if InputTiming.pressKeySettle > 0 {
            Thread.sleep(forTimeInterval: InputTiming.pressKeySettle)
        }
    }

}
