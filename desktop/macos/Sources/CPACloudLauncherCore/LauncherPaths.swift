import Foundation

public struct LauncherPaths: Equatable, Sendable {
    public let applicationSupportDirectory: URL
    public let dataDirectory: URL
    public let lockFile: URL
    public let serverExecutable: URL
    public let webDirectory: URL

    public init(applicationSupportDirectory: URL, resourceDirectory: URL) {
        self.applicationSupportDirectory = applicationSupportDirectory
        self.dataDirectory = applicationSupportDirectory.appendingPathComponent("data", isDirectory: true)
        self.lockFile = applicationSupportDirectory.appendingPathComponent("launcher.lock", isDirectory: false)
        self.serverExecutable = resourceDirectory
            .appendingPathComponent("server", isDirectory: true)
            .appendingPathComponent("cpa-cloud", isDirectory: false)
        self.webDirectory = resourceDirectory.appendingPathComponent("web", isDirectory: true)
    }

    public static func installed(
        resourceDirectory: URL,
        homeDirectory: URL = FileManager.default.homeDirectoryForCurrentUser
    ) -> LauncherPaths {
        let support = homeDirectory
            .appendingPathComponent("Library", isDirectory: true)
            .appendingPathComponent("Application Support", isDirectory: true)
            .appendingPathComponent("CPACloud", isDirectory: true)
        return LauncherPaths(applicationSupportDirectory: support, resourceDirectory: resourceDirectory)
    }

    public func validatePackagedResources(fileManager: FileManager = .default) throws {
        guard fileManager.isExecutableFile(atPath: serverExecutable.path) else {
            throw LauncherPathError.serverMissing(serverExecutable.path)
        }
        var isDirectory: ObjCBool = false
        guard fileManager.fileExists(atPath: webDirectory.path, isDirectory: &isDirectory), isDirectory.boolValue else {
            throw LauncherPathError.webDirectoryMissing(webDirectory.path)
        }
        guard fileManager.fileExists(atPath: webDirectory.appendingPathComponent("index.html").path) else {
            throw LauncherPathError.webIndexMissing(webDirectory.path)
        }
    }
}

public enum LauncherPathError: LocalizedError, Equatable {
    case serverMissing(String)
    case webDirectoryMissing(String)
    case webIndexMissing(String)

    public var errorDescription: String? {
        switch self {
        case .serverMissing(let path):
            return "The bundled CPA Cloud server is missing or not executable: \(path)"
        case .webDirectoryMissing(let path):
            return "The bundled web directory is missing: \(path)"
        case .webIndexMissing(let path):
            return "The bundled web directory does not contain index.html: \(path)"
        }
    }
}
