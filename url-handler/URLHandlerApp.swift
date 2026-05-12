import AppKit
import Foundation

// Minimal URL handler app for the "ghostosaur://" scheme.
// Converts ghostosaur://claude/tf → http://localhost:7071/s/claude/tf
// and opens it in Chrome (falls back to default browser).
//
// Registered in Info.plist as a background-only agent (LSUIElement = true)
// so it never shows a dock icon or menu bar.

final class AppDelegate: NSObject, NSApplicationDelegate {

    // Register the Apple Event handler before the app finishes launching so
    // that URLs delivered during launch are not missed.
    func applicationWillFinishLaunching(_ notification: Notification) {
        NSAppleEventManager.shared().setEventHandler(
            self,
            andSelector: #selector(handleGetURL(_:withReplyEvent:)),
            forEventClass: AEEventClass(kInternetEventClass),
            andEventID: AEEventID(kAEGetURL)
        )
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // If no URL event arrives within 1 s (e.g. accidental double-click),
        // quit silently.
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) {
            NSApp.terminate(nil)
        }
    }

    @objc func handleGetURL(
        _ event: NSAppleEventDescriptor,
        withReplyEvent reply: NSAppleEventDescriptor
    ) {
        guard
            let urlString = event.paramDescriptor(forKeyword: AEKeyword(keyDirectObject))?.stringValue,
            let url = URL(string: urlString)
        else { return }

        let path = extractPath(from: url)
        let target = "http://localhost:7071/s/\(path)"

        openInChrome(target)
        NSApp.terminate(nil)
    }

    // ghostosaur://claude/tf  → host="claude", path="/tf"  → "claude/tf"
    // ghostosaur://new        → host="new",    path=""     → "new"
    private func extractPath(from url: URL) -> String {
        var parts: [String] = []
        if let host = url.host, !host.isEmpty { parts.append(host) }
        let rest = url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if !rest.isEmpty { parts.append(rest) }
        return parts.joined(separator: "/")
    }

    private func openInChrome(_ urlString: String) {
        guard let url = URL(string: urlString) else { return }

        let chromePaths = [
            "/Applications/Google Chrome.app",
            "/Applications/Google Chrome Dev.app",
            "/Applications/Google Chrome Beta.app",
            "/Applications/Chromium.app",
        ]

        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = true

        for chromePath in chromePaths {
            let appURL = URL(fileURLWithPath: chromePath)
            guard FileManager.default.fileExists(atPath: chromePath) else { continue }
            NSWorkspace.shared.open(
                [url],
                withApplicationAt: appURL,
                configuration: cfg
            )
            return
        }

        // Fallback to whatever the system default browser is.
        NSWorkspace.shared.open(url)
    }
}

// ── Entry point ────────────────────────────────────────────────────────────
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.prohibited) // no Dock icon
app.run()
