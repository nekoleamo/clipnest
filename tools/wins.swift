// 窗口层级探针：CGWindowList 列出匹配 owner 的窗口。
// 用法: swift tools/wins.swift [owner 子串]   （默认 nspike）
// 输出: num=窗口号 layer=层级 onscreen=是否在屏 alpha=透明度 bounds=区域
// 层级 3 = floating panel（nsh 的 WindowKind::Popup）；num 可直接喂 `screencapture -l`。
import CoreGraphics

let a = CommandLine.arguments
let needle = (a.count > 1 ? a[1] : "nspike").lowercased()
let opts: CGWindowListOption = [.optionAll]
if let list = CGWindowListCopyWindowInfo(opts, kCGNullWindowID) as? [[String: Any]] {
    var found = 0
    for w in list {
        let owner = (w[kCGWindowOwnerName as String] as? String) ?? ""
        if owner.lowercased().contains(needle) {
            found += 1
            let num = w[kCGWindowNumber as String] ?? "?"
            let layer = w[kCGWindowLayer as String] ?? "?"
            let onscreen = w[kCGWindowIsOnscreen as String] ?? "?"
            let bounds = w[kCGWindowBounds as String] ?? "?"
            let alpha = w[kCGWindowAlpha as String] ?? "?"
            print(
                "num=\(num) owner=\(owner) layer=\(layer) onscreen=\(onscreen) alpha=\(alpha) bounds=\(bounds)"
            )
        }
    }
    if found == 0 { print("no window of owner containing \(needle)") }
} else {
    print("CGWindowListCopyWindowInfo failed")
}
