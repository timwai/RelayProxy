import AppKit
import WebKit

final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler {
    private let lifecycleMessageName = "relayproxyLifecycle"
    private let rdpMessageName = "relayproxyRDP"
    private let outputQueue = DispatchQueue(label: "com.relayproxy.desktop.agent-output")
    private var outputBuffer = Data()
    private var agent: Process?
    private var restartRequested = false
    private var outputPipe: Pipe?
    private var managementURL: URL?
    private var startupTimeout: DispatchWorkItem?
    private var window: NSWindow!
    private var webView: WKWebView!
    private var statusItem: NSStatusItem!
    private var agentStateItem: NSMenuItem!
    private var reloadItem: NSMenuItem!
    private var copyAddressItem: NSMenuItem!
    private var messagePollTimer: Timer?
    private var messagePollInFlight = false
    private var seenMessageIDs = Set<String>()
    private let messageBaselineMillis = Int64(Date().timeIntervalSince1970 * 1000)

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        configureMenu()
        configureWindow()
        configureStatusItem()
        startAgent()
        NSApp.activate(ignoringOtherApps: true)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showWindow(nil)
        return true
    }

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        sender.orderOut(nil)
        return false
    }

    func applicationWillTerminate(_ notification: Notification) {
        startupTimeout?.cancel()
        messagePollTimer?.invalidate()
        messagePollTimer = nil
        webView?.configuration.userContentController.removeScriptMessageHandler(forName: lifecycleMessageName)
        webView?.configuration.userContentController.removeScriptMessageHandler(forName: rdpMessageName)
        outputPipe?.fileHandleForReading.readabilityHandler = nil
        if let agent, agent.isRunning {
            agent.terminate()
        }
    }

    private func configureWindow() {
        if let iconURL = Bundle.main.url(forResource: "AppIcon", withExtension: "png"),
           let icon = NSImage(contentsOf: iconURL) {
            NSApp.applicationIconImage = icon
        }

        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .default()
        configuration.userContentController.add(self, name: lifecycleMessageName)
        configuration.userContentController.add(self, name: rdpMessageName)
        webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = self
        webView.uiDelegate = self

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

    private func configureStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        if let button = statusItem.button {
            let symbols = ["point.3.connected.trianglepath.dotted", "network"]
            let configuration = NSImage.SymbolConfiguration(pointSize: 15, weight: .semibold)
            if let image = symbols.compactMap({ NSImage(systemSymbolName: $0, accessibilityDescription: "RelayProxy") }).first?
                .withSymbolConfiguration(configuration) {
                image.isTemplate = true
                button.image = image
            } else {
                button.title = "RP"
            }
            button.imageScaling = .scaleProportionallyDown
            button.toolTip = "RelayProxy"
            button.setAccessibilityLabel("RelayProxy")
        }

        let menu = NSMenu()
        menu.autoenablesItems = false

        let titleItem = NSMenuItem()
        titleItem.attributedTitle = NSAttributedString(
            string: "RelayProxy",
            attributes: [.font: NSFont.systemFont(ofSize: 13, weight: .semibold)]
        )
        titleItem.isEnabled = false
        menu.addItem(titleItem)

        agentStateItem = NSMenuItem(title: "Agent 正在启动…", action: nil, keyEquivalent: "")
        agentStateItem.isEnabled = false
        menu.addItem(agentStateItem)
        menu.addItem(.separator())

        let showItem = NSMenuItem(title: "打开控制台", action: #selector(showWindow(_:)), keyEquivalent: "o")
        showItem.target = self
        showItem.image = menuImage("macwindow")
        menu.addItem(showItem)

        reloadItem = NSMenuItem(title: "刷新管理界面", action: #selector(reloadManagementPage(_:)), keyEquivalent: "r")
        reloadItem.target = self
        reloadItem.image = menuImage("arrow.clockwise")
        reloadItem.isEnabled = false
        menu.addItem(reloadItem)

        copyAddressItem = NSMenuItem(title: "复制管理地址", action: #selector(copyManagementAddress(_:)), keyEquivalent: "")
        copyAddressItem.target = self
        copyAddressItem.image = menuImage("doc.on.doc")
        copyAddressItem.isEnabled = false
        menu.addItem(copyAddressItem)

        menu.addItem(.separator())

        let aboutItem = NSMenuItem(title: "关于 RelayProxy", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        aboutItem.target = NSApp
        aboutItem.image = menuImage("info.circle")
        menu.addItem(aboutItem)

        menu.addItem(.separator())
        let quitItem = NSMenuItem(title: "退出 RelayProxy", action: #selector(quitApplication(_:)), keyEquivalent: "q")
        quitItem.target = self
        quitItem.image = menuImage("power")
        menu.addItem(quitItem)
        statusItem.menu = menu
    }

    private func menuImage(_ systemName: String) -> NSImage? {
        let image = NSImage(systemSymbolName: systemName, accessibilityDescription: nil)
        image?.isTemplate = true
        return image
    }

    private func updateAgentState(_ title: String, symbol: String, tooltip: String) {
        agentStateItem?.title = title
        agentStateItem?.image = menuImage(symbol)
        statusItem?.button?.toolTip = tooltip
    }

    @objc private func showWindow(_ sender: Any?) {
        if window.isMiniaturized {
            window.deminiaturize(sender)
        }
        window.makeKeyAndOrderFront(sender)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc private func reloadManagementPage(_ sender: Any?) {
        guard let managementURL else { return }
        webView.load(URLRequest(url: managementURL, cachePolicy: .reloadIgnoringLocalCacheData))
        showWindow(sender)
    }

    @objc private func copyManagementAddress(_ sender: Any?) {
        guard let managementURL else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(managementURL.absoluteString, forType: .string)
    }

    @objc private func quitApplication(_ sender: Any?) {
        NSApp.terminate(sender)
    }

    private func launchWindowsAppRDP(_ address: String) {
        let endpoint = address.trimmingCharacters(in: .whitespacesAndNewlines)
        let prefix = "127.0.0.1:"
        guard endpoint.hasPrefix(prefix),
              let port = Int(endpoint.dropFirst(prefix.count)),
              (1...65535).contains(port) else {
            presentRDPLaunchError("RelayProxy 返回了无效的本地 RDP 地址：\(address)")
            return
        }

        // Windows App is stricter than the retired Microsoft Remote Desktop
        // client when LaunchServices parses the legacy rdp:// scheme. Encode the
        // attribute separators after the equals sign as %3A; otherwise `open`
        // reports the URI as an uninterpretable path or URL.
        let rdpURI = "rdp://full%20address=s%3A127.0.0.1%3A\(port)"
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let attempts = [
                ["-n", "-a", "/Applications/Windows App.app", rdpURI],
                ["-b", "com.microsoft.rdc.macos", rdpURI],
                ["-a", "Windows App", rdpURI],
                [rdpURI],
            ]
            var failures: [String] = []
            for arguments in attempts {
                if let failure = self?.runOpen(arguments) {
                    failures.append(failure)
                    continue
                }
                return
            }
            DispatchQueue.main.async {
                let detail = failures.last ?? "LaunchServices 未能打开 RDP 地址。"
                self?.presentRDPLaunchError("Windows App 启动失败：\(detail)")
            }
        }
    }

    private func runOpen(_ arguments: [String]) -> String? {
        let process = Process()
        let errorPipe = Pipe()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        process.arguments = arguments
        process.standardError = errorPipe
        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return error.localizedDescription
        }
        if process.terminationStatus == 0 {
            return nil
        }
        let data = errorPipe.fileHandleForReading.readDataToEndOfFile()
        let message = String(data: data, encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return (message?.isEmpty == false) ? message! : "open 退出码 \(process.terminationStatus)"
    }

    private func presentRDPLaunchError(_ detail: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "无法打开 Windows App"
        alert.informativeText = detail
        alert.addButton(withTitle: "知道了")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.webView === webView, message.frameInfo.isMainFrame else { return }

        if message.name == rdpMessageName,
           let body = message.body as? [String: Any],
           let address = body["address"] as? String {
            launchWindowsAppRDP(address)
            return
        }

        guard message.name == lifecycleMessageName, let action = message.body as? String else { return }
        if action == "quit" { quitApplication(nil); return }
        if action == "restart" {
            guard !restartRequested, let agent, agent.isRunning else { return }
            restartRequested = true
            updateAgentState("正在重启 Agent…", symbol: "arrow.clockwise", tooltip: "RelayProxy · 正在重启")
            agent.terminate()
        }
    }

    func webView(
        _ webView: WKWebView,
        runJavaScriptConfirmPanelWithMessage message: String,
        initiatedByFrame frame: WKFrameInfo,
        completionHandler: @escaping (Bool) -> Void
    ) {
        let alert = NSAlert()
        alert.messageText = "RelayProxy"
        alert.informativeText = message
        alert.alertStyle = .warning
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")
        completionHandler(alert.runModal() == .alertFirstButtonReturn)
    }

    func webView(
        _ webView: WKWebView,
        runJavaScriptAlertPanelWithMessage message: String,
        initiatedByFrame frame: WKFrameInfo,
        completionHandler: @escaping () -> Void
    ) {
        let alert = NSAlert()
        alert.messageText = "RelayProxy"
        alert.informativeText = message
        alert.addButton(withTitle: "确定")
        alert.runModal()
        completionHandler()
    }

    private func startAgent() {
        guard let executable = bundledAgentURL() else {
            updateAgentState("Agent 不可用", symbol: "exclamationmark.circle", tooltip: "RelayProxy · 缺少 relay-agent")
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
            updateAgentState("Agent 正在连接…", symbol: "circle.dotted", tooltip: "RelayProxy · Agent 正在连接")
            let timeout = DispatchWorkItem { [weak self] in
                guard let self, self.managementURL == nil, process.isRunning else { return }
                self.updateAgentState("管理界面不可用", symbol: "exclamationmark.circle", tooltip: "RelayProxy · 管理界面未启动")
                self.showStatus(
                    title: "本地管理界面未启动",
                    detail: "请确认配置中的 Web 管理功能已启用，且监听端口未被其他程序占用。"
                )
            }
            startupTimeout = timeout
            DispatchQueue.main.asyncAfter(deadline: .now() + 12, execute: timeout)
        } catch {
            pipe.fileHandleForReading.readabilityHandler = nil
            updateAgentState("Agent 启动失败", symbol: "exclamationmark.circle", tooltip: "RelayProxy · Agent 启动失败")
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
            self.reloadItem.isEnabled = true
            self.copyAddressItem.isEnabled = true
            self.updateAgentState("Agent 正常运行", symbol: "checkmark.circle", tooltip: "RelayProxy · Agent 正常运行")
            self.webView.load(URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData))
            self.startMessagePolling()
        }
    }

    private struct RelayMessage: Decodable {
        let id: String
        let title: String?
        let content: String?
        let verificationCode: String?
        let verificationRule: String?
        let popup: Bool?
        let popupType: String?
        let source: String?
        let createdAt: Int64?

        var effectivePopupType: String {
            let value = popupType?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if !value.isEmpty { return value }
            return (verificationCode?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false)
                ? "verification_code"
                : "message"
        }

        var shouldPopup: Bool {
            let type = popupType?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if type.isEmpty {
                return verificationCode?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
            }
            return popup == true
        }
    }

    private func messageAPIURL() -> URL? {
        guard let managementURL,
              var components = URLComponents(url: managementURL, resolvingAgainstBaseURL: false) else { return nil }
        let basePath = components.path.hasSuffix("/") ? String(components.path.dropLast()) : components.path
        components.path = basePath + "/api/messages"
        return components.url
    }

    private func startMessagePolling() {
        messagePollTimer?.invalidate()
        pollMessages()
        messagePollTimer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            self?.pollMessages()
        }
    }

    private func pollMessages() {
        guard !messagePollInFlight, let url = messageAPIURL() else { return }
        messagePollInFlight = true
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 3)
        request.httpMethod = "GET"
        URLSession.shared.dataTask(with: request) { [weak self] data, _, error in
            DispatchQueue.main.async {
                guard let self else { return }
                self.messagePollInFlight = false
                guard error == nil, let data,
                      let messages = try? JSONDecoder().decode([RelayMessage].self, from: data) else { return }
                self.consumeMessages(messages)
            }
        }.resume()
    }

    private func consumeMessages(_ messages: [RelayMessage]) {
        for message in messages {
            let isNew = seenMessageIDs.insert(message.id).inserted
            guard isNew, message.shouldPopup else { continue }
            if let createdAt = message.createdAt, createdAt + 1_000 < messageBaselineMillis {
                continue
            }
            presentMessagePopup(message)
        }
    }

    private func presentMessagePopup(_ message: RelayMessage) {
        let type = message.effectivePopupType
        let code = message.verificationCode?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let content = message.content?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let source = message.source?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let rule = message.verificationRule?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let title = message.title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""

        let alert = NSAlert()
        switch type {
        case "important":
            alert.alertStyle = .warning
            alert.messageText = title.isEmpty ? "RelayProxy 重要提醒" : "重要提醒 · \(title)"
        case "message":
            alert.alertStyle = .informational
            alert.messageText = title.isEmpty ? "RelayProxy 消息" : title
        default:
            alert.alertStyle = .informational
            alert.messageText = title.isEmpty ? "收到新的验证码" : title
        }

        var details: [String] = []
        if type == "verification_code", !code.isEmpty {
            details.append("验证码：\(code)")
        }
        if !content.isEmpty {
            details.append(content)
        }
        if type == "important", !code.isEmpty {
            details.append("验证码：\(code)")
        }
        if !source.isEmpty {
            details.append("来源：\(source)")
        }
        if !rule.isEmpty {
            details.append("匹配规则：\(rule)")
        }
        alert.informativeText = details.isEmpty ? "收到一条 RelayProxy 消息" : details.joined(separator: "\n\n")

        let canCopy = !code.isEmpty && (type == "verification_code" || type == "important")
        if canCopy {
            alert.addButton(withTitle: "复制验证码")
            alert.addButton(withTitle: "关闭")
        } else {
            alert.addButton(withTitle: "知道了")
        }

        if type == "important" {
            NSApp.requestUserAttention(.criticalRequest)
        }
        NSApp.activate(ignoringOtherApps: true)
        let response = alert.runModal()
        if canCopy && response == .alertFirstButtonReturn {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(code, forType: .string)
        }
    }

    private func agentDidTerminate(_ process: Process) {
        outputPipe?.fileHandleForReading.readabilityHandler = nil
        startupTimeout?.cancel()
        messagePollTimer?.invalidate()
        messagePollTimer = nil
        messagePollInFlight = false
        agent = nil
        reloadItem?.isEnabled = false
        copyAddressItem?.isEnabled = false
        if restartRequested {
            restartRequested = false
            managementURL = nil
            seenMessageIDs.removeAll()
            startAgent()
            return
        }
        if managementURL != nil && process.terminationStatus == 0 {
            NSApp.terminate(nil)
            return
        }
        managementURL = nil
        updateAgentState("Agent 已停止", symbol: "exclamationmark.circle", tooltip: "RelayProxy · Agent 已停止")
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
