import Darwin
import Foundation

public protocol PortAvailabilityChecking {
    func isAvailable(port: UInt16) -> Bool
}

public struct LoopbackPortProbe: PortAvailabilityChecking {
    public init() {}

    public func isAvailable(port: UInt16) -> Bool {
        let descriptor = Darwin.socket(AF_INET, SOCK_STREAM, 0)
        guard descriptor >= 0 else { return false }
        defer { Darwin.close(descriptor) }

        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = in_port_t(port).bigEndian
        address.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))

        let result = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { socketAddress in
                Darwin.bind(descriptor, socketAddress, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        return result == 0
    }
}

public enum ServiceLaunchError: LocalizedError, Equatable {
    case portInUse(UInt16)
    case processFailedToStart(String)
    case readinessTimedOut

    public var errorDescription: String? {
        switch self {
        case .portInUse(let port):
            return "Port \(port) is already in use. CPA Cloud did not stop or connect to the occupying process."
        case .processFailedToStart(let reason):
            return "CPA Cloud could not start: \(reason)"
        case .readinessTimedOut:
            return "CPA Cloud started but did not become ready in time."
        }
    }
}
