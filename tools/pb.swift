// 测试用剪贴板写入器：往系统剪贴板放不同 flavor，供 nspike 抓取验证。
// 用法: pb html|rtf|text|image|file [内容]
import AppKit

let args = CommandLine.arguments
let mode = args.count > 1 ? args[1] : "text"
let payload = args.count > 2 ? args[2] : "default"

let pb = NSPasteboard.general
pb.clearContents()

switch mode {
case "html":
    let html = "<b>\(payload)</b> <i>html-flavor</i>"
    pb.declareTypes([.html, .string], owner: nil)
    pb.setString(html, forType: .html)
    pb.setString(payload, forType: .string)
case "rtf":
    let attr = NSAttributedString(
        string: payload,
        attributes: [.font: NSFont.boldSystemFont(ofSize: 14)]
    )
    let data = try! attr.data(
        from: NSRange(location: 0, length: attr.length),
        documentAttributes: [.documentType: NSAttributedString.DocumentType.rtf]
    )
    pb.declareTypes([.rtf, .string], owner: nil)
    pb.setData(data, forType: .rtf)
    pb.setString(payload, forType: .string)
case "image":
    let img = NSImage(size: NSSize(width: 64, height: 64))
    img.lockFocus()
    NSColor.systemPink.setFill()
    NSRect(x: 0, y: 0, width: 64, height: 64).fill()
    img.unlockFocus()
    pb.declareTypes([.tiff, .png], owner: nil)
    if let tiff = img.tiffRepresentation {
        pb.setData(tiff, forType: .tiff)
    }
case "file":
    let url = URL(fileURLWithPath: payload)
    pb.declareTypes([.fileURL], owner: nil)
    pb.setString(url.absoluteString, forType: .fileURL)
case "files":
    let urls = [URL(fileURLWithPath: "/etc/hosts"), URL(fileURLWithPath: "/etc/passwd")]
    pb.declareTypes([.fileURL], owner: nil)
    _ = pb.writeObjects(urls as [NSURL])
default:
    pb.declareTypes([.string], owner: nil)
    pb.setString(payload, forType: .string)
}
print("set clipboard: \(mode) \(payload)")
