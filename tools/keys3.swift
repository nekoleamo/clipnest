// 合成按键：给 clipnest 的焦点/粘贴流程做自动验证。
// 用法: swift tools/keys3.swift <动作>
//   combo       ⌘⇧V（全局快捷键）
//   paste       ⌘V
//   enter/escape/down/up/backspace/x
//   type:hello  逐字符输入（US 布局，够测英文搜索）
import AppKit
import ApplicationServices

let a = CommandLine.arguments
let what = a.count > 1 ? a[1] : "x"

/// US 布局的字符 → keycode。
func keycode(for c: Character) -> CGKeyCode? {
    let table: [Character: CGKeyCode] = [
        "a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9,
        "b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17,
        "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25,
        "7": 26, "-": 27, "8": 28, "0": 29, "]": 30, "o": 31, "u": 32, "[": 33,
        "i": 34, "p": 35, "l": 37, "j": 38, "'": 39, "k": 40, ";": 41, "\\": 42,
        ",": 43, "/": 44, "n": 45, "m": 46, ".": 47, " ": 49, "`": 50,
    ]
    return table[c]
}

// 首字符输入模式：必须在默认分支之前处理，否则会先多按一个 x。
if what.hasPrefix("type:") {
    let text = String(what.dropFirst(5))
    let src = CGEventSource(stateID: .combinedSessionState)!
    for ch in text {
        guard let code = keycode(for: ch) else { continue }
        let down = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: true)!
        let up = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: false)!
        down.flags = []
        up.flags = []
        down.post(tap: .cghidEventTap)
        usleep(40_000)
        up.post(tap: .cghidEventTap)
        usleep(40_000)
    }
    print("typed \(text)")
    exit(0)
}

// ⌘+字符 / ⌘⇧+字符：三次测试动作键位用。
if what.hasPrefix("mod:") || what.hasPrefix("modshift:") {
    let shift = what.hasPrefix("modshift:")
    let text = String(what.dropFirst(shift ? 9 : 4))
    guard let ch = text.first, let code = keycode(for: ch) else {
        print("unknown key \(text)")
        exit(1)
    }
    let src = CGEventSource(stateID: .combinedSessionState)!
    var flags: CGEventFlags = [.maskCommand]
    if shift { flags.insert(.maskShift) }
    let down = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: true)!
    let up = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: false)!
    down.flags = flags
    up.flags = flags
    down.post(tap: .cghidEventTap)
    usleep(40_000)
    up.post(tap: .cghidEventTap)
    print("posted \(what)")
    exit(0)
}

let (code, flags): (CGKeyCode, CGEventFlags) = {
    switch what {
    case "combo": return (9, [.maskCommand, .maskShift])
    case "paste": return (9, [.maskCommand])
    case "x": return (7, [])           // kVK_ANSI_X
    case "down": return (125, [])      // kVK_DownArrow
    case "up": return (126, [])        // kVK_UpArrow
    case "left": return (123, [])      // kVK_LeftArrow
    case "right": return (124, [])     // kVK_RightArrow
    case "enter": return (36, [])      // kVK_Return
    case "escape": return (53, [])     // kVK_Escape
    case "backspace": return (51, [])  // kVK_Delete
    default: return (7, [])
    }
}()
let src = CGEventSource(stateID: .combinedSessionState)
let d = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: true)!
let u = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: false)!
d.flags = flags
u.flags = flags
d.post(tap: .cghidEventTap)
usleep(40_000)
u.post(tap: .cghidEventTap)
print("posted \(what)")
