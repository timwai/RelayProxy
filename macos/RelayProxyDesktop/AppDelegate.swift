import AppKit
import WebKit

final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate {
    private let outputQueue = DispatchQueue(label: "com.relayproxy.desktop.agent-output")
    private var outputBuffer = Data()
    private var agent: Process?
    private var outputPipe: Pipe?
    private var managementURL: URL?
    private var startupTimeout: DispatchWorkItem?
    private var window: NSWindow!
    private var webView: WKWebView!

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        configureMenu()
        configureWindow()
        startAgent()
        NSApp.activate(ignoringOtherApps: true)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationWillTerminate(_ notification: Notification) {
        startupTimeout?.cancel()
        outputPipe?.fileHandleForReading.readabilityHandler = nil
        if let agent, agent.isRunning {
            agent.terminate()
        }
    }

    private func configureWindow() {
        if let iconURL = Bundle.main.url(forResource: "logo", withExtension: "png"),
           let icon = NSImage(contentsOf: iconURL) {
            NSApp.applicationIconImage = icon
        }

        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .default()
        webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = self

        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1100, height: 760),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "RelayProxy"
        window.minSize = NSSize(width: 820, height: 560)
        window.center()
        window.contentView = webView
        window.delegate = self
        window.makeKeyAndOrderFront(nil)
        showStatus(title: "正在启动 RelayProxy", detail: "正在准备本地管理界面…")
    }

    private func configureMenu() {
        let mainMenu = NSMenu()
        let appItem = NSMenuItem()
        mainMenu.addItem(appItem)
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "关于 RelayProxy", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "退出 RelayProxy", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu

        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "编辑")
        editMenu.addItem(withTitle: "撤销", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "重做", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "剪切", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "复制", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "粘贴", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "全选", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = editMenu
        NSApp.mainMenu = mainMenu
    }

    private func startAgent() {
        guard let executable = bundledAgentURL() else {
            showStatus(title: "无法启动 RelayProxy", detail: "应用包中缺少 relay-agent。请重新构建或安装完整的 RelayProxy.app。")
            return
        }

        let process = Process()
        let pipe = Pipe()
        process.executableURL = executable
        process.arguments = ["--no-gui"] + forwardedArguments()
        process.standardOutput = pipe
        process.standardError = pipe
        process.terminationHandler = { [weak self] process in
            DispatchQueue.main.async { self?.agentDidTerminate(process) }
        }
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else { return }
            self?.consumeAgentOutput(data)
        }

        do {
            try process.run()
            agent = process
            outputPipe = pipe
            let timeout = DispatchWorkItem { [weak self] in
                guard let self, self.managementURL == nil, process.isRunning else { return }
                self.showStatus(
                    title: "本地管理界面未启动",
                    detail: "请确认配置中的 Web 管理功能已启用，且监听端口未被其他程序占用。"
                )
            }
            startupTimeout = timeout
            DispatchQueue.main.asyncAfter(deadline: .now() + 12, execute: timeout)
        } catch {
            pipe.fileHandleForReading.readabilityHandler = nil
            showStatus(title: "无法启动 RelayProxy", detail: error.localizedDescription)
        }
    }

    private func bundledAgentURL() -> URL? {
        if let configured = ProcessInfo.processInfo.environment["RELAYPROXY_AGENT_PATH"], !configured.isEmpty {
            let url = URL(fileURLWithPath: configured)
            if FileManager.default.isExecutableFile(atPath: url.path) { return url }
        }
        if let url = Bundle.main.url(forResource: "relay-agent", withExtension: nil),
           FileManager.default.isExecutableFile(atPath: url.path) {
            return url
        }
        return nil
    }

    private func forwardedArguments() -> [String] {
        var result: [String] = []
        for argument in CommandLine.arguments.dropFirst() {
            if argument.hasPrefix("-psn_") || argument == "--gui" || argument == "--no-gui" { continue }
            result.append(argument)
        }
        return result
    }

    private func consumeAgentOutput(_ data: Data) {
        outputQueue.async { [weak self] in
            guard let self else { return }
            self.outputBuffer.append(data)
            while let newline = self.outputBuffer.firstIndex(of: 0x0A) {
                let lineData = self.outputBuffer.prefix(upTo: newline)
                self.outputBuffer.removeSubrange(...newline)
                guard let line = String(data: lineData, encoding: .utf8) else { continue }
                self.consumeAgentLine(line)
            }
        }
    }

    private func consumeAgentLine(_ line: String) {
        let marker = "[Web] Management page: "
        guard let range = line.range(of: marker) else { return }
        let rawURL = line[range.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: rawURL), url.host?.isEmpty == false else { return }
        DispatchQueue.main.async { [weak self] in
            guard let self, self.managementURL == nil else { return }
            self.startupTimeout?.cancel()
            self.managementURL = url
            self.webView.load(URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData))
        }
    }

    private func agentDidTerminate(_ process: Process) {
        outputPipe?.fileHandleForReading.readabilityHandler = nil
        startupTimeout?.cancel()
        if managementURL != nil {
            NSApp.terminate(nil)
            return
        }
        let detail = process.terminationStatus == 0
            ? "另一个 RelayProxy 实例可能已在运行。请先退出旧实例后重试。"
            : "relay-agent 已退出，状态码：\(process.terminationStatus)。请从终端启动以查看完整诊断。"
        showStatus(title: "RelayProxy 启动失败", detail: detail)
    }

    private func showStatus(title: String, detail: String) {
        let escapedTitle = htmlEscape(title)
        let escapedDetail = htmlEscape(detail)
        let html = """
        <!doctype html><html lang="zh-CN"><meta charset="utf-8">
        <style>
        :root { color-scheme: light dark; font-family: -apple-system, BlinkMacSystemFont, sans-serif; }
        body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f6f8fa; color: #17212b; }
        main { max-width: 560px; padding: 44px; text-align: center; }
        h1 { font-size: 24px; margin: 0 0 12px; } p { line-height: 1.65; color: #607080; }
        .pulse { width: 36px; height: 36px; margin: 0 auto 24px; border: 3px solid #ccefe4; border-top-color: #13a879; border-radius: 50%; animation: spin 1s linear infinite; }
        @keyframes spin { to { transform: rotate(360deg); } }
        @media (prefers-color-scheme: dark) { body { background: #111820; color: #eef4f7; } p { color: #9dafba; } }
        </style><body><main><div class="pulse"></div><h1>\(escapedTitle)</h1><p>\(escapedDetail)</p></main></body></html>
        """
        webView.loadHTMLString(html, baseURL: nil)
    }

    private func htmlEscape(_ value: String) -> String {
        value.replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
            .replacingOccurrences(of: "\"", with: "&quot;")
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        showStatus(title: "管理界面加载失败", detail: error.localizedDescription)
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        showStatus(title: "管理界面加载失败", detail: error.localizedDescription)
    }
}
