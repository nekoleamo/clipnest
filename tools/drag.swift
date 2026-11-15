// 模拟一次鼠标拖动：在 (x,y) 按下，移动 (dx,dy)，松开。
// 用法: swift tools/drag.swift <x> <y> <dx> <dy>
import CoreGraphics
import Foundation

let a = CommandLine.arguments
guard a.count >= 5 else { print("用法: drag <x> <y> <dx> <dy>"); exit(1) }
let x = Double(a[1])!, y = Double(a[2])!
let dx = Double(a[3])!, dy = Double(a[4])!

func post(_ type: CGEventType, _ p: CGPoint) {
    guard let src = CGEventSource(stateID: .combinedSessionState) else { return }
    let e = CGEvent(mouseEventSource: src, mouseType: type, mouseCursorPosition: p, mouseButton: .left)!
    e.post(tap: .cghidEventTap)
}

post(.mouseMoved, CGPoint(x: x, y: y))
usleep(120_000)
post(.leftMouseDown, CGPoint(x: x, y: y))
usleep(120_000)
let steps = 12
for i in 1...steps {
    let t = Double(i) / Double(steps)
    post(.leftMouseDragged, CGPoint(x: x + dx * t, y: y + dy * t))
    usleep(25_000)
}
usleep(120_000)
post(.leftMouseUp, CGPoint(x: x + dx, y: y + dy))
print("dragged")
