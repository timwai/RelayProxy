import Foundation
import Network
import NetworkExtension

final class Provider: NETransparentProxyProvider {
    private let queue = DispatchQueue(label: "com.relayproxy.network-extension", qos: .userInitiated)
	private let stateLock = NSLock()
    private var handlers: [ObjectIdentifier: AnyObject] = [:]
    private var stopped = false
    private var agentReady = false
	private var controlConnection: IPCConnection?
	private var ipcToken = ""

    private var sharedDirectory: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: "group.com.relayproxy.shared")
    }

    private var socketPath: String { sharedDirectory!.appendingPathComponent("agent.sock").path }
    private var token: String {
        (try? String(contentsOf: sharedDirectory!.appendingPathComponent("ipc.token"), encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)) ?? ""
    }

    override func startProxy(options: [String : Any]? = nil, completionHandler: @escaping (Error?) -> Void) {
		let loadedToken = token
		guard sharedDirectory != nil, loadedToken.count >= 32 else {
            completionHandler(IPCError.authenticationFailed)
            return
        }
		ipcToken = loadedToken
        let settings = NETransparentProxyNetworkSettings(tunnelRemoteAddress: "127.0.0.1")
        settings.includedNetworkRules = [
			NENetworkRule(remoteNetwork: NWHostEndpoint(hostname: "0.0.0.0", port: "0"), remotePrefix: 0,
			              localNetwork: nil, localPrefix: 0, protocol: .any, direction: .outbound),
			NENetworkRule(remoteNetwork: NWHostEndpoint(hostname: "::", port: "0"), remotePrefix: 0,
			              localNetwork: nil, localPrefix: 0, protocol: .any, direction: .outbound),
        ]
        setTunnelNetworkSettings(settings) { [weak self] error in
            guard let self else { completionHandler(error); return }
            if let error { completionHandler(error); return }
			self.stateLock.lock()
			self.stopped = false
			self.agentReady = false
			self.stateLock.unlock()
			completionHandler(nil)
			self.monitorAgent()
        }
    }

    private func monitorAgent() {
        let connection = IPCConnection(socketPath: socketPath, queue: queue)
		stateLock.lock()
		if stopped || controlConnection != nil {
			stateLock.unlock()
			return
		}
        controlConnection = connection
		stateLock.unlock()
		connection.start(role: "control", token: ipcToken) { [weak self, weak connection] result in
			guard let self, let connection else { return }
            switch result {
            case .success:
				if self.markAgentReady(connection) { self.waitForAgentDisconnect(connection) }
            case .failure:
                connection.cancel()
				if self.detachControlConnection(connection) {
					self.retryAgentMonitor(after: .milliseconds(200))
				}
            }
        }
    }

	private func markAgentReady(_ connection: IPCConnection) -> Bool {
		stateLock.lock()
		defer { stateLock.unlock() }
		guard !stopped, controlConnection === connection else { return false }
		agentReady = true
		return true
	}

	// Returns true only when monitoring should be retried.
	private func detachControlConnection(_ connection: IPCConnection) -> Bool {
		stateLock.lock()
		defer { stateLock.unlock() }
		guard controlConnection === connection else { return false }
		agentReady = false
		controlConnection = nil
		return !stopped
	}

	private func waitForAgentDisconnect(_ connection: IPCConnection) {
		connection.receiveRaw { [weak self, weak connection] _, complete, error in
			guard let self, let connection else { return }
			if error != nil || complete {
				connection.cancel()
				if self.detachControlConnection(connection) {
					self.retryAgentMonitor(after: .milliseconds(200))
				}
				return
			}
			self.stateLock.lock()
			let current = !self.stopped && self.controlConnection === connection
			self.stateLock.unlock()
			if current { self.waitForAgentDisconnect(connection) }
		}
	}

	private func retryAgentMonitor(after delay: DispatchTimeInterval) {
		queue.asyncAfter(deadline: .now() + delay) { [weak self] in self?.monitorAgent() }
	}

    override func stopProxy(with reason: NEProviderStopReason, completionHandler: @escaping () -> Void) {
		stateLock.lock()
        stopped = true
		agentReady = false
		let control = controlConnection
		controlConnection = nil
        handlers.removeAll()
		stateLock.unlock()
		control?.cancel()
        completionHandler()
    }

    override func handleNewFlow(_ flow: NEAppProxyFlow) -> Bool {
		// The provider remains fail-open while the Go Agent is unavailable. The
		// authenticated control stream flips this before any flow is claimed.
		stateLock.lock()
		guard agentReady, !stopped else {
			stateLock.unlock()
			return false
		}
		let currentToken = ipcToken
		stateLock.unlock()
        let identifier = ObjectIdentifier(flow)
		let finish: () -> Void = { [weak self] in
			guard let self else { return }
			self.stateLock.lock()
			self.handlers.removeValue(forKey: identifier)
			self.stateLock.unlock()
		}
        if let tcp = flow as? NEAppProxyTCPFlow {
			let handler = TCPFlowHandler(flow: tcp, socketPath: socketPath, token: currentToken, queue: queue, finish: finish)
			stateLock.lock()
            handlers[identifier] = handler
			stateLock.unlock()
            handler.start()
            return true
        }
        if let udp = flow as? NEAppProxyUDPFlow {
			let handler = UDPFlowHandler(flow: udp, socketPath: socketPath, token: currentToken, queue: queue, finish: finish)
			stateLock.lock()
            handlers[identifier] = handler
			stateLock.unlock()
            handler.start()
            return true
        }
        return false
    }
}

private func endpointParts(_ endpoint: NWEndpoint?) -> (String, UInt16)? {
    guard case let .hostPort(host, port) = endpoint else { return nil }
    let value = String(describing: host)
	guard IPv4Address(value) != nil || IPv6Address(value) != nil else { return nil }
	return (value, port.rawValue)
}

private func flowIdentity(_ flow: NEAppProxyFlow) -> String {
    let signing = flow.metaData.sourceAppSigningIdentifier
    return signing.isEmpty ? "unknown.macos.application" : signing
}

// NEAppProxyTCPFlow exposes the original remote endpoint but no local
// endpoint. The Agent only needs a stable, nonzero source to identify this
// already-distinct IPC flow, so derive one from Apple's flow UUID instead of
// inventing or opening another socket. This endpoint is never used for I/O.
private func syntheticTCPSource(_ flow: NEAppProxyTCPFlow, destinationIP: String) -> (String, UInt16) {
	let identifier = flow.metaData.filterFlowIdentifier?.uuidString ?? String(ObjectIdentifier(flow).hashValue)
	var hash: UInt32 = 2_166_136_261
	for byte in identifier.utf8 { hash = (hash ^ UInt32(byte)) &* 16_777_619 }
	let port = UInt16(1_024 + (hash % 64_511))
	return (IPv6Address(destinationIP) == nil ? "127.0.0.1" : "::1", port)
}

private final class TCPFlowHandler {
    let flow: NEAppProxyTCPFlow
    let socketPath: String
    let token: String
    let queue: DispatchQueue
    let finish: () -> Void
    var ipc: IPCConnection?
    var direct: NWConnection?

    init(flow: NEAppProxyTCPFlow, socketPath: String, token: String, queue: DispatchQueue, finish: @escaping () -> Void) {
        self.flow = flow; self.socketPath = socketPath; self.token = token; self.queue = queue; self.finish = finish
    }

    func start() {
		let remoteEndpoint = flow.remoteEndpoint
		guard let destination = endpointParts(remoteEndpoint) else {
            close(IPCError.invalidEndpoint); return
        }
		let source = syntheticTCPSource(flow, destinationIP: destination.0)
        let connection = IPCConnection(socketPath: socketPath, queue: queue)
        ipc = connection
        connection.start(role: "flow", token: token) { [weak self] result in
            guard let self else { return }
            if case .failure(let error) = result { self.close(error); return }
            let open = IPCOpenFlow(version: IPCConnection.version, protocol: "tcp", source_ip: source.0,
                                    source_port: source.1, destination_ip: destination.0,
                                    destination_port: destination.1, process_id: 0,
									process: flowIdentity(self.flow), hostname: self.flow.remoteHostname)
            connection.sendJSON(.open, open) { error in
                if let error { self.close(error); return }
				connection.receiveFrame { decision in self.handleDecision(decision, destination: remoteEndpoint) }
            }
        }
    }

    private func handleDecision(_ result: Result<(IPCFrameKind, Data), Error>, destination: NWEndpoint) {
        guard case .success(let frame) = result, frame.0 == .decision,
              let decision = try? JSONDecoder().decode(IPCDecision.self, from: frame.1) else {
            close(IPCError.invalidFrame); return
        }
        switch decision.action {
        case "PROXY":
            flow.open(withLocalEndpoint: nil) { [weak self] error in
                if let error { self?.close(error); return }
                self?.pumpApplicationToIPC(); self?.pumpIPCToApplication()
            }
        case "DIRECT":
            ipc?.cancel(); ipc = nil
            let upstream = NWConnection(to: destination, using: .tcp)
            direct = upstream
            upstream.stateUpdateHandler = { [weak self] state in
                guard let self else { return }
                switch state {
                case .ready:
                    self.flow.open(withLocalEndpoint: nil) { error in
                        if let error { self.close(error); return }
                        self.pumpApplicationToDirect(); self.pumpDirectToApplication()
                    }
                case .failed(let error): self.close(error)
                default: break
                }
            }
            upstream.start(queue: queue)
        default:
            close(nil)
        }
    }

    private func pumpApplicationToIPC() {
        flow.readData { [weak self] data, error in
            guard let self else { return }
            if let error { self.close(error); return }
            guard let data, !data.isEmpty else { self.close(nil); return }
            self.ipc?.sendRaw(data) { error in
                if let error { self.close(error) } else { self.pumpApplicationToIPC() }
            }
        }
    }

    private func pumpIPCToApplication() {
        ipc?.receiveRaw { [weak self] data, complete, error in
            guard let self else { return }
            if let error { self.close(error); return }
            guard let data, !data.isEmpty else { if complete { self.close(nil) }; return }
            self.flow.write(data) { error in
                if let error { self.close(error) } else { self.pumpIPCToApplication() }
            }
        }
    }

    private func pumpApplicationToDirect() {
        flow.readData { [weak self] data, error in
            guard let self else { return }
            if let error { self.close(error); return }
            guard let data, !data.isEmpty else { self.close(nil); return }
            self.direct?.send(content: data, completion: .contentProcessed { error in
                if let error { self.close(error) } else { self.pumpApplicationToDirect() }
            })
        }
    }

    private func pumpDirectToApplication() {
        direct?.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, complete, error in
            guard let self else { return }
            if let error { self.close(error); return }
            guard let data, !data.isEmpty else { if complete { self.close(nil) }; return }
            self.flow.write(data) { error in
                if let error { self.close(error) } else { self.pumpDirectToApplication() }
            }
        }
    }

    private func close(_ error: Error?) {
        ipc?.cancel(); direct?.cancel()
        flow.closeReadWithError(error); flow.closeWriteWithError(error)
        finish()
    }
}

private final class UDPFlowHandler {
    let flow: NEAppProxyUDPFlow
    let socketPath: String
    let token: String
    let queue: DispatchQueue
    let finish: () -> Void
    var ipc: IPCConnection?
    var direct: [String: NWConnection] = [:]

    init(flow: NEAppProxyUDPFlow, socketPath: String, token: String, queue: DispatchQueue, finish: @escaping () -> Void) {
        self.flow = flow; self.socketPath = socketPath; self.token = token; self.queue = queue; self.finish = finish
    }

    func start() {
        guard let source = endpointParts(flow.localEndpoint) else { close(IPCError.invalidEndpoint); return }
        let connection = IPCConnection(socketPath: socketPath, queue: queue)
        ipc = connection
        connection.start(role: "flow", token: token) { [weak self] result in
            guard let self else { return }
            if case .failure(let error) = result { self.close(error); return }
            let open = IPCOpenFlow(version: IPCConnection.version, protocol: "udp", source_ip: source.0,
                                   source_port: source.1, destination_ip: nil, destination_port: nil,
                                   process_id: 0, process: flowIdentity(self.flow), hostname: nil)
            connection.sendJSON(.open, open) { error in
                if let error { self.close(error); return }
                self.flow.open(withLocalEndpoint: nil) { error in
                    if let error { self.close(error); return }
                    self.readApplication(); self.readAgent()
                }
            }
        }
    }

    private func readApplication() {
        flow.readDatagrams { [weak self] datagrams, endpoints, error in
            guard let self else { return }
            if let error { self.close(error); return }
            guard let datagrams, let endpoints, datagrams.count == endpoints.count else {
                self.close(IPCError.invalidEndpoint); return
            }
			self.sendDatagrams(datagrams, endpoints: endpoints, index: 0)
        }
    }

	private func sendDatagrams(_ datagrams: [Data], endpoints: [NWEndpoint], index: Int) {
		guard index < datagrams.count else { readApplication(); return }
		guard let ipc else { close(IPCError.connectionFailed("IPC unavailable")); return }
		let datagram = IPCEndpointDatagram(endpoint: endpoints[index], payload: datagrams[index])
		ipc.sendDatagram(.udpDatagram, datagram) { [weak self] error in
			guard let self else { return }
			if let error { self.close(error); return }
			// Do not read another application batch until every previous send has
			// completed. This is the bounded-memory flow control Apple recommends
			// for Network Extension providers.
			self.sendDatagrams(datagrams, endpoints: endpoints, index: index + 1)
		}
	}

    private func readAgent() {
        ipc?.receiveFrame { [weak self] result in
            guard let self else { return }
            guard case .success(let frame) = result,
                  let datagram = try? IPCEndpointDatagram.decode(frame.1) else {
                if case .failure(let error) = result { self.close(error) } else { self.close(IPCError.invalidFrame) }
                return
            }
            switch frame.0 {
            case .udpReply:
                self.flow.writeDatagrams([datagram.payload], sentBy: [datagram.endpoint]) { error in
                    if let error { self.close(error) }
                }
            case .udpDirect:
                self.sendDirect(datagram)
            case .udpReject:
                break
            default:
                self.close(IPCError.invalidFrame); return
            }
            self.readAgent()
        }
    }

    private func sendDirect(_ datagram: IPCEndpointDatagram) {
        let key = String(describing: datagram.endpoint)
        let connection: NWConnection
        if let existing = direct[key] {
            connection = existing
        } else {
            connection = NWConnection(to: datagram.endpoint, using: .udp)
            direct[key] = connection
            connection.stateUpdateHandler = { [weak self, weak connection] state in
                if case .ready = state, let connection { self?.receiveDirect(connection, endpoint: datagram.endpoint) }
                if case .failed(let error) = state { self?.close(error) }
            }
            connection.start(queue: queue)
        }
        connection.send(content: datagram.payload, completion: .contentProcessed { [weak self] error in
            if let error { self?.close(error) }
        })
    }

    private func receiveDirect(_ connection: NWConnection, endpoint: NWEndpoint) {
        connection.receiveMessage { [weak self, weak connection] data, _, _, error in
            guard let self, let connection else { return }
            if let error { self.close(error); return }
            if let data {
                self.flow.writeDatagrams([data], sentBy: [endpoint]) { error in
                    if let error { self.close(error) }
                }
            }
            self.receiveDirect(connection, endpoint: endpoint)
        }
    }

    private func close(_ error: Error?) {
        ipc?.cancel(); direct.values.forEach { $0.cancel() }; direct.removeAll()
        flow.closeReadWithError(error); flow.closeWriteWithError(error)
        finish()
    }
}
