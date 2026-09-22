import Darwin
import Foundation

public struct ServerCLI {
    public let executable: URL
    public let commands: ServerCommands
    public let operationTimeout: TimeInterval

    public init(executable: URL, commands: ServerCommands, operationTimeout: TimeInterval = 10) {
        self.executable = executable
        self.commands = commands
        self.operationTimeout = operationTimeout
    }

    public func checkInitialization() throws -> InitializationStatus {
        try InitializationStatus.from(exitCode: run(arguments: commands.checkInitialized, stdin: nil))
    }

    public func initialize(password: String) throws {
        let exitCode = try run(arguments: commands.initialize, stdin: Data(password.utf8))
        guard exitCode == 0 else {
            throw ServerCLIError.initializationFailed(exitCode: exitCode)
        }
    }

    private func run(arguments: [String], stdin: Data?) throws -> Int32 {
        let process = Process()
        process.executableURL = executable
        process.arguments = arguments
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice

        var inputPipe: Pipe?
        if stdin != nil {
            let pipe = Pipe()
            process.standardInput = pipe
            inputPipe = pipe
        }

        do {
            try process.run()
        } catch {
            throw ServerCLIError.launchFailed(error.localizedDescription)
        }

        if let stdin, let inputPipe {
            inputPipe.fileHandleForWriting.write(stdin)
            try? inputPipe.fileHandleForWriting.close()
        }
        let deadline = Date().addingTimeInterval(operationTimeout)
        while process.isRunning && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.05)
        }
        if process.isRunning {
            process.terminate()
            let terminationDeadline = Date().addingTimeInterval(1)
            while process.isRunning && Date() < terminationDeadline {
                Thread.sleep(forTimeInterval: 0.05)
            }
            if process.isRunning {
                Darwin.kill(process.processIdentifier, SIGKILL)
                let killDeadline = Date().addingTimeInterval(1)
                while process.isRunning && Date() < killDeadline {
                    Thread.sleep(forTimeInterval: 0.05)
                }
            }
            throw ServerCLIError.timedOut
        }
        return process.terminationStatus
    }
}

public enum ServerCLIError: LocalizedError, Equatable {
    case launchFailed(String)
    case initializationFailed(exitCode: Int32)
    case timedOut

    public var errorDescription: String? {
        switch self {
        case .launchFailed(let reason):
            return "The bundled CPA Cloud server could not be launched: \(reason)"
        case .initializationFailed(let exitCode):
            return "CPA Cloud initialization failed (exit code \(exitCode))."
        case .timedOut:
            return "CPA Cloud did not finish the requested local operation in time."
        }
    }
}
