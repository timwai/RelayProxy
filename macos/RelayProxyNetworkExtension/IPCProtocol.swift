import Foundation
import Network

enum IPCFrameKind: UInt8 {
    case hello = 1
    case open = 2
    case decision = 3
    case error = 4
    case udpDatagram = 10
    case udpDirect = 11
    case udpReply = 12
    case udpReject = 13
}

struct IPCHello: Codable {
    let version: Int
    let token: String
    let role: String
}

struct IPCOpenFlow: Codable {
    let version: Int
    let `protocol`: String
    let source_ip: String
    let source_port: UInt16
    let destination_ip: String?
    let destination_port: UInt16?
    let process_id: UInt32
    let process: String
    let hostname: String?
}

struct IPCDecision: Codable {
    let action: String
    let exit_id: String?
    let reason: String?
}

enum IPCError: Error {
    case invalidFrame
    case invalidEndpoint
    case authenticationFailed
    case connectionFailed(String)
}

final class IPCConnection {
    static let version = 1
    static let maxFrame = 2 * 1024 * 1024

    private let connection: NWConnection
    private let queue: DispatchQueue

    init(socketPath: String, queue: DispatchQueue) {
        self.queue = queue
        self.connection = NWConnection(to: .unix(path: socketPath), using: .tcp)
    }

    func start(role: String, token: String, completion: @escaping (Result<Void, Error>) -> Void) {
        var completed = false
        connection.stateUpdateHandler = { [weak self] state in
            guard let self else { return }
            switch state {
            case .ready where !completed:
                completed = true
                do {
                    let body = try JSONEncoder().encode(IPCHello(version: Self.version, token: token, role: role))
                    self.sendFrame(.hello, body) { error in
                        if let error { completion(.failure(error)); return }
                        self.receiveFrame { result in
                            switch result {
                            case .success(let frame) where frame.0 == .hello:
                                completion(.success(()))
                            case .success:
                                completion(.failure(IPCError.authenticationFailed))
                            case .failure(let error):
                                completion(.failure(error))
                            }
                        }
                    }
                } catch {
                    completion(.failure(error))
                }
            case .failed(let error) where !completed:
                completed = true
                completion(.failure(error))
            case .cancelled where !completed:
                completed = true
                completion(.failure(IPCError.connectionFailed("cancelled")))
            default:
                break
            }
        }
        connection.start(queue: queue)
    }

    func cancel() {
        connection.cancel()
    }

    func sendJSON<T: Encodable>(_ kind: IPCFrameKind, _ value: T, completion: @escaping (Error?) -> Void) {
        do {
            sendFrame(kind, try JSONEncoder().encode(value), completion: completion)
        } catch {
            completion(error)
        }
    }

    func sendFrame(_ kind: IPCFrameKind, _ body: Data, completion: @escaping (Error?) -> Void) {
		sendFrame(kind, parts: [body], completion: completion)
	}

	func sendDatagram(_ kind: IPCFrameKind, _ datagram: IPCEndpointDatagram,
	                  completion: @escaping (Error?) -> Void) {
		do {
			// Keep the endpoint prefix and datagram payload in separate Data
			// values. Sequential Network.framework sends preserve stream order
			// without allocating another payload-sized concatenation buffer.
			sendFrame(kind, parts: [try datagram.encodeHeader(), datagram.payload], completion: completion)
		} catch {
			completion(error)
		}
	}

	private func sendFrame(_ kind: IPCFrameKind, parts: [Data], completion: @escaping (Error?) -> Void) {
		let bodyLength = parts.reduce(0) { $0 + $1.count }
		guard bodyLength <= Self.maxFrame else { completion(IPCError.invalidFrame); return }
		var length = UInt32(bodyLength).bigEndian
		var header = Data(capacity: 5)
		header.append(kind.rawValue)
		withUnsafeBytes(of: &length) { header.append(contentsOf: $0) }
		var chunks = [header]
		chunks.append(contentsOf: parts)
		sendChunks(chunks, index: 0, completion: completion)
	}

	private func sendChunks(_ chunks: [Data], index: Int, completion: @escaping (Error?) -> Void) {
		guard index < chunks.count else { completion(nil); return }
		connection.send(content: chunks[index], completion: .contentProcessed { [weak self] error in
			if let error { completion(error); return }
			guard let self else { completion(IPCError.connectionFailed("connection released")); return }
			self.sendChunks(chunks, index: index + 1, completion: completion)
		})
	}

    func receiveFrame(completion: @escaping (Result<(IPCFrameKind, Data), Error>) -> Void) {
        receiveExactly(5) { [weak self] result in
            guard let self else { return }
            switch result {
            case .failure(let error): completion(.failure(error))
            case .success(let header):
                guard let kind = IPCFrameKind(rawValue: header[header.startIndex]) else {
                    completion(.failure(IPCError.invalidFrame)); return
                }
                let length = header.dropFirst().withUnsafeBytes { raw in
                    raw.loadUnaligned(as: UInt32.self).bigEndian
                }
                guard length <= Self.maxFrame else { completion(.failure(IPCError.invalidFrame)); return }
                self.receiveExactly(Int(length)) { body in completion(body.map { (kind, $0) }) }
            }
        }
    }

    func sendRaw(_ data: Data, completion: @escaping (Error?) -> Void) {
        connection.send(content: data, completion: .contentProcessed(completion))
    }

    func receiveRaw(completion: @escaping (Data?, Bool, Error?) -> Void) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { data, _, complete, error in
            completion(data, complete, error)
        }
    }

    private func receiveExactly(_ count: Int, completion: @escaping (Result<Data, Error>) -> Void) {
        if count == 0 { completion(.success(Data())); return }
		var collected = Data(capacity: count)
        func next() {
            connection.receive(minimumIncompleteLength: count - collected.count, maximumLength: count - collected.count) {
                data, _, complete, error in
                if let data { collected.append(data) }
                if let error { completion(.failure(error)); return }
                if collected.count == count { completion(.success(collected)); return }
                if complete { completion(.failure(IPCError.invalidFrame)); return }
                next()
            }
        }
        next()
    }
}

struct IPCEndpointDatagram {
    let endpoint: NWEndpoint
    let payload: Data

    func encodeHeader() throws -> Data {
        guard case let .hostPort(host, port) = endpoint,
              let address = IPvBytes(String(describing: host)) else { throw IPCError.invalidEndpoint }
		var data = Data(capacity: 3 + address.count)
		data.append(UInt8(address.count))
        var rawPort = port.rawValue.bigEndian
        withUnsafeBytes(of: &rawPort) { data.append(contentsOf: $0) }
        data.append(contentsOf: address)
        return data
    }

    static func decode(_ data: Data) throws -> IPCEndpointDatagram {
        guard data.count >= 7 else { throw IPCError.invalidEndpoint }
        let addressLength = Int(data[data.startIndex])
        guard (addressLength == 4 || addressLength == 16), data.count >= 3 + addressLength else {
            throw IPCError.invalidEndpoint
        }
        let portValue = data.dropFirst().prefix(2).withUnsafeBytes { raw in
            raw.loadUnaligned(as: UInt16.self).bigEndian
        }
        let address = [UInt8](data.dropFirst(3).prefix(addressLength))
        let host: NWEndpoint.Host
        if addressLength == 4 {
			host = .ipv4(IPv4Address(Data(address))!)
        } else {
			host = .ipv6(IPv6Address(Data(address))!)
        }
        return IPCEndpointDatagram(endpoint: .hostPort(host: host, port: NWEndpoint.Port(rawValue: portValue)!),
                                   payload: data.dropFirst(3 + addressLength))
    }
}

private func IPvBytes(_ raw: String) -> [UInt8]? {
    if let address = IPv4Address(raw) { return Array(address.rawValue) }
    if let address = IPv6Address(raw) { return Array(address.rawValue) }
    return nil
}
