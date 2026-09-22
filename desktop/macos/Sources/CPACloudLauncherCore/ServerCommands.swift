import Foundation

public struct ServerCommands: Equatable, Sendable {
    public let paths: LauncherPaths
    public let listenAddress: String

    public init(paths: LauncherPaths, listenAddress: String = "127.0.0.1:8787") {
        self.paths = paths
        self.listenAddress = listenAddress
    }

    public var checkInitialized: [String] {
        ["--check-initialized", "--data-dir", paths.dataDirectory.path]
    }

    public var initialize: [String] {
        ["--data-dir", paths.dataDirectory.path, "--init"]
    }

    public func runService(instanceID: UUID) -> [String] {
        [
            "--data-dir", paths.dataDirectory.path,
            "--listen", listenAddress,
            "--web-dir", paths.webDirectory.path,
            "--instance-id", instanceID.uuidString.lowercased(),
            "--shutdown-on-stdin-eof",
        ]
    }
}

public enum InitializationStatus: Equatable, Sendable {
    case initialized
    case notInitialized

    public static func from(exitCode: Int32) throws -> InitializationStatus {
        switch exitCode {
        case 0:
            return .initialized
        case 3:
            return .notInitialized
        default:
            throw InitializationCheckError.failed(exitCode: exitCode)
        }
    }
}

public enum InitializationCheckError: LocalizedError, Equatable {
    case failed(exitCode: Int32)

    public var errorDescription: String? {
        switch self {
        case .failed(let exitCode):
            return "CPA Cloud could not verify its data directory (exit code \(exitCode))."
        }
    }
}
