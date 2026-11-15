// 粘贴目标：自带 Edit 菜单（⌘V 是菜单快捷键），粘贴后把内容打印出来。
import AppKit

final class AppDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var textView: NSTextView!

    func applicationDidFinishLaunching(_ note: Notification) {
        // Edit 菜单：让 ⌘V 有 paste: 的 key equivalent
        let mainMenu = NSMenu()
        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Paste", action: Selector(("paste:")), keyEquivalent: "v")
        editItem.submenu = editMenu
        NSApp.mainMenu = mainMenu

        window = NSWindow(contentRect: NSRect(x: 200, y: 200, width: 500, height: 200),
                          styleMask: [.titled, .closable],
                          backing: .buffered, defer: false)
        window.title = "PasteTarget"
        textView = NSTextView(frame: NSRect(x: 0, y: 0, width: 500, height: 200))
        textView.isEditable = true
        textView.isRichText = false
        window.contentView = textView
        window.makeKeyAndOrderFront(nil)
        window.makeFirstResponder(textView)
        NSApp.activate(ignoringOtherApps: true)

        DispatchQueue.main.asyncAfter(deadline: .now() + 2.0) {
            NSLog("before paste: key=\(self.window.isKeyWindow) first=\(String(describing: self.window.firstResponder))")
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 11.0) {
            print("TEXT=[\(self.textView.string)]")
            fflush(stdout)
            NSApp.terminate(nil)
        }
    }
}

let app = NSApplication.shared
app.setActivationPolicy(.regular)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
