import CPACloudLauncherCore
import Darwin
import Foundation

enum ServiceState: Equatable {
    case stopped
    case starting
    case running
    case stopping
    case failed(String)
}

final class ServerProcessController {
    var onStateChange: ((ServiceState) -> Void)?

    private(set) var state: ServiceState = .stopped {
        didSet { onStateChange?(state) }
    }

    private let paths: LauncherPaths
    private let commands: ServerCommands
    private let portProbe: PortAvailabilityChecking
    private let healthURL = URL(string: "http://127.0.0.1:8787/healthz")!
    private let healthSession: URLSession
    private var process: Process?
    private var standardInput: Pipe?
    private var activeInstanceID: UUID?

    init(
        paths: LauncherPaths,
        portProbe: PortAvailabilityChecking = LoopbackPortProbe()
    ) {
        self.paths = paths
        self.commands = ServerCommands(paths: paths)
        self.portProbe = portProbe
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 1
        configuration.connectionProxyDictionary = [:]
        self.healthSession = URLSession(configuration: configuration)
    }

    var ownsRunningService: Bool {
        process?.isRunning == true
    }

    var isReady: Bool {
        state == .running && ownsRunningService
    }

    func start(onReady: @escaping () -> Void) throws {
        guard !ownsRunningService else { return }
        guard portProbe.isAvailable(port: 8787) else {
            throw ServiceLaunchError.portInUse(8787)
        }
        try paths.validatePackagedResources()
        try SecureDirectory.ensure(paths.dataDirectory)

        let child = Process()
        let input = Pipe()
        child.executableURL = paths.serverExecutable
        let instanceID = UUID()
        child.arguments = commands.runService(instanceID: instanceID)
        child.standardInput = input
        child.standardOutput = FileHandle.nullDevice
        child.standardError = FileHandle.nullDevice
        child.terminationHandler = { [weak self, weak child] terminated in
            DispatchQueue.main.async {
                guard let self, let child, self.process === child else { return }
                let expected = self.state == .stopping
                self.releaseProcess()
                if expected || terminated.terminationStatus == 0 {
                    self.state = .stopped
                } else {
                    self.state = .failed("服务意外退出（代码 \(terminated.terminationStatus)）。")
                }
            }
        }

        process = child
        standardInput = input
        activeInstanceID = instanceID
        state = .starting
        do {
            try child.run()
        } catch {
            releaseProcess()
            state = .failed("无法启动服务。")
            throw ServiceLaunchError.processFailedToStart(error.localizedDescription)
        }
        pollHealth(for: child, instanceID: instanceID, deadline: Date().addingTimeInterval(10), onReady: onReady)
    }

    func stop(completion: @escaping () -> Void) {
        guard let child = process else {
            state = .stopped
            completion()
            return
        }
        guard child.isRunning else {
            releaseProcess()
            state = .stopped
            completion()
            return
        }

        state = .stopping
        try? standardInput?.fileHandleForWriting.close()
        let processID = child.processIdentifier

        DispatchQueue.global(qos: .utility).async { [weak self, weak child] in
            guard let child else {
                DispatchQueue.main.async { completion() }
                return
            }

            Self.wait(for: child, seconds: 5)
            if child.isRunning {
                child.terminate()
                Self.wait(for: child, seconds: 2)
            }
            if child.isRunning {
                Darwin.kill(processID, SIGKILL)
                Self.wait(for: child, seconds: 1)
            }

            DispatchQueue.main.async {
                guard let self else {
                    completion()
                    return
                }
                if self.process === child {
                    self.releaseProcess()
                    self.state = .stopped
                }
                completion()
            }
        }
    }

    private func pollHealth(for child: Process, instanceID: UUID, deadline: Date, onReady: @escaping () -> Void) {
        guard state == .starting,
              process === child,
              child.isRunning,
              activeInstanceID == instanceID
        else { return }
        var request = URLRequest(url: healthURL)
        request.cachePolicy = .reloadIgnoringLocalCacheData
        healthSession.dataTask(with: request) { [weak self, weak child] data, response, _ in
            DispatchQueue.main.async {
                guard let self, let child,
                      self.state == .starting,
                      self.process === child,
                      child.isRunning,
                      self.activeInstanceID == instanceID
                else { return }
                let statusCode = (response as? HTTPURLResponse)?.statusCode
                let healthy = statusCode == 200 && Self.isHealthyResponse(data, instanceID: instanceID)
                if healthy {
                    self.state = .running
                    onReady()
                    return
                }
                if Date() < deadline {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) {
                        self.pollHealth(for: child, instanceID: instanceID, deadline: deadline, onReady: onReady)
                    }
                } else {
                    self.stop {
                        self.state = .failed(ServiceLaunchError.readinessTimedOut.localizedDescription)
                    }
                }
            }
        }.resume()
    }

    private static func isHealthyResponse(_ data: Data?, instanceID: UUID) -> Bool {
        guard let data,
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return false }
        return object["status"] as? String == "ok"
            && object["instance_id"] as? String == instanceID.uuidString.lowercased()
    }

    private static func wait(for process: Process, seconds: TimeInterval) {
        let deadline = Date().addingTimeInterval(seconds)
        while process.isRunning && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.05)
        }
    }

    private func releaseProcess() {
        try? standardInput?.fileHandleForWriting.close()
        standardInput = nil
        activeInstanceID = nil
        process = nil
    }
}
