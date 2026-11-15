import AppKit
import ApplicationServices
let a = CommandLine.arguments
let x = Double(a[1])!, y = Double(a[2])!
let p = CGPoint(x: x, y: y)
for type in [CGEventType.leftMouseDown, .leftMouseUp] {
    let e = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: p, mouseButton: .left)!
    e.post(tap: .cghidEventTap)
    usleep(60_000)
}
print("clicked \(x),\(y)")
