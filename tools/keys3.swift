import AppKit
import ApplicationServices
let a = CommandLine.arguments
let what = a.count > 1 ? a[1] : "x"
let (code, flags): (CGKeyCode, CGEventFlags) = {
    switch what {
    case "combo": return (9, [.maskCommand, .maskShift])
    case "paste": return (9, [.maskCommand])
    case "x": return (7, [])          // kVK_ANSI_X
    default: return (7, [])
    }
}()
let src = CGEventSource(stateID: .combinedSessionState)
let d = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: true)!
let u = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: false)!
d.flags = flags; u.flags = flags
d.post(tap: .cghidEventTap); usleep(40_000); u.post(tap: .cghidEventTap)
print("posted \(what)")
