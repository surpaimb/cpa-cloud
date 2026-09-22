import CPACloudLauncherShim
import Darwin
import Foundation

public final class SingleInstanceLock {
    private var descriptor: Int32 = -1

    public init(lockFile: URL) throws {
        try SecureDirectory.ensure(lockFile.deletingLastPathComponent())
        let fd = Darwin.open(lockFile.path, O_CREAT | O_RDWR | O_CLOEXEC, S_IRUSR | S_IWUSR)
        guard fd >= 0 else {
            throw SingleInstanceError.lockUnavailable
        }
        _ = Darwin.fcntl(fd, F_SETFD, FD_CLOEXEC)
        guard cpa_cloud_flock(fd, LOCK_EX | LOCK_NB) == 0 else {
            Darwin.close(fd)
            throw SingleInstanceError.alreadyRunning
        }
        descriptor = fd
    }

    deinit {
        if descriptor >= 0 {
            _ = cpa_cloud_flock(descriptor, LOCK_UN)
            Darwin.close(descriptor)
        }
    }
}

public enum SingleInstanceError: LocalizedError, Equatable {
    case alreadyRunning
    case lockUnavailable

    public var errorDescription: String? {
        switch self {
        case .alreadyRunning:
            return "CPA Cloud Launcher is already running for this user."
        case .lockUnavailable:
            return "CPA Cloud Launcher could not create its single-instance lock."
        }
    }
}
