import CoreGraphics
let opts: CGWindowListOption = [.optionAll]
if let list = CGWindowListCopyWindowInfo(opts, kCGNullWindowID) as? [[String: Any]] {
    var found = 0
    for w in list {
        let owner = (w[kCGWindowOwnerName as String] as? String) ?? ""
        if owner.lowercased().contains("nspike") {
            found += 1
            let layer = w[kCGWindowLayer as String] ?? "?"
            let onscreen = w[kCGWindowIsOnscreen as String] ?? "?"
            let bounds = w[kCGWindowBounds as String] ?? "?"
            let alpha = w[kCGWindowAlpha as String] ?? "?"
            print("owner=\(owner) layer=\(layer) onscreen=\(onscreen) alpha=\(alpha) bounds=\(bounds)")
        }
    }
    print("windows owned by nspike: \(found)")
}
