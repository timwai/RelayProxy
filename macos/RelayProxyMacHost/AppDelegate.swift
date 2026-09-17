import AppKit
import CryptoKit
import NetworkExtension
import OSSystemExtension

@main
final class AppDelegate: NSObject, NSApplicationDelegate, OSSystemExtensionRequestDelegate {
    private let extensionIdentifier = "com.relayproxy.agent.network-extension"

    func applicationDidFinishLaunching(_ notification: Notification) {
        do {
            try provisionToken()
        } catch {
            present(error)
            return
        }
        let request = OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier: extensionIdentifier, queue: .main)
        request.delegate = self
        OSSystemExtensionManager.shared.submitRequest(request)
    }

    private func provisionToken() throws {
        guard let directory = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "group.com.relayproxy.shared") else {
            throw NSError(domain: "RelayProxy", code: 1, userInfo: [NSLocalizedDescriptionKey: "无法访问 RelayProxy App Group"])
        }
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        let tokenURL = directory.appendingPathComponent("ipc.token")
        if !FileManager.default.fileExists(atPath: tokenURL.path) {
            let token = SymmetricKey(size: .bits256).withUnsafeBytes { Data($0).base64EncodedString() }
            try token.write(to: tokenURL, atomically: true, encoding: .utf8)
        }
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: tokenURL.path)
    }

    func request(_ request: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
        NETransparentProxyManager.loadAllFromPreferences { managers, error in
            if let error { self.present(error); return }
            let manager = managers?.first ?? NETransparentProxyManager()
            let provider = NETunnelProviderProtocol()
            provider.providerBundleIdentifier = self.extensionIdentifier
            provider.serverAddress = "RelayProxy local Agent"
            manager.localizedDescription = "RelayProxy 透明代理"
            manager.providerProtocol = provider
            manager.isEnabled = true
            manager.saveToPreferences { error in
                if let error { self.present(error); return }
                manager.loadFromPreferences { error in
                    if let error { self.present(error); return }
                    do {
                        try manager.connection.startVPNTunnel()
                        let alert = NSAlert()
                        alert.messageText = "RelayProxy Network Extension 已启用"
                        alert.informativeText = "请启动 relay-agent；Web 管理默认位于 http://127.0.0.1:9090/。"
                        alert.runModal()
                    } catch {
                        self.present(error)
                    }
                }
            }
        }
    }

    func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) { present(error) }

    func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
        let alert = NSAlert()
        alert.messageText = "需要批准 RelayProxy Network Extension"
        alert.informativeText = "请在系统设置的隐私与安全性页面批准扩展，然后重新打开本程序。"
        alert.runModal()
    }

    func request(_ request: OSSystemExtensionRequest,
                 actionForReplacingExtension existing: OSSystemExtensionProperties,
                 withExtension extension: OSSystemExtensionProperties) -> OSSystemExtensionRequest.ReplacementAction {
        .replace
    }

    private func present(_ error: Error) {
        let alert = NSAlert(error: error)
        alert.runModal()
    }
}
