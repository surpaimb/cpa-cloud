import Darwin
import Foundation
import XCTest
@testable import CPACloudLauncherCore

final class PasswordValidatorTests: XCTestCase {
    func testAcceptsInclusiveUTF8ByteBoundaries() throws {
        try PasswordValidator.validate(String(repeating: "a", count: 12), confirmation: String(repeating: "a", count: 12))
        try PasswordValidator.validate(String(repeating: "b", count: 72), confirmation: String(repeating: "b", count: 72))
    }

    func testCountsUTF8BytesInsteadOfCharacters() throws {
        let twelveBytes = String(repeating: "密", count: 4)
        XCTAssertEqual(twelveBytes.lengthOfBytes(using: .utf8), 12)
        try PasswordValidator.validate(twelveBytes, confirmation: twelveBytes)
    }

    func testRejectsOutOfRangeAndMismatchedPasswords() {
        XCTAssertThrowsError(try PasswordValidator.validate(String(repeating: "a", count: 11), confirmation: String(repeating: "a", count: 11)))
        XCTAssertThrowsError(try PasswordValidator.validate(String(repeating: "a", count: 73), confirmation: String(repeating: "a", count: 73)))
        XCTAssertThrowsError(try PasswordValidator.validate("abcdefghijkl", confirmation: "abcdefghijklM")) { error in
            XCTAssertEqual(error as? PasswordValidationError, .mismatch)
        }
    }
}

final class ServerCommandsTests: XCTestCase {
    private let support = URL(fileURLWithPath: "/Users/test/Library/Application Support/CPACloud", isDirectory: true)
    private let resources = URL(fileURLWithPath: "/Applications/CPA Cloud.app/Contents/Resources", isDirectory: true)

    func testBuildsExactServiceProtocolArguments() throws {
        let paths = LauncherPaths(applicationSupportDirectory: support, resourceDirectory: resources)
        let commands = ServerCommands(paths: paths)
        let instanceID = try XCTUnwrap(UUID(uuidString: "123E4567-E89B-12D3-A456-426614174000"))

        XCTAssertEqual(commands.checkInitialized, [
            "--check-initialized", "--data-dir", "/Users/test/Library/Application Support/CPACloud/data",
        ])
        XCTAssertEqual(commands.initialize, [
            "--data-dir", "/Users/test/Library/Application Support/CPACloud/data", "--init",
        ])
        XCTAssertEqual(commands.runService(instanceID: instanceID), [
            "--data-dir", "/Users/test/Library/Application Support/CPACloud/data",
            "--listen", "127.0.0.1:8787",
            "--web-dir", "/Applications/CPA Cloud.app/Contents/Resources/web",
            "--instance-id", "123e4567-e89b-12d3-a456-426614174000",
            "--shutdown-on-stdin-eof",
        ])
    }

    func testMapsInitializationExitCodes() throws {
        XCTAssertEqual(try InitializationStatus.from(exitCode: 0), .initialized)
        XCTAssertEqual(try InitializationStatus.from(exitCode: 3), .notInitialized)
        XCTAssertThrowsError(try InitializationStatus.from(exitCode: 1)) { error in
            XCTAssertEqual(error as? InitializationCheckError, .failed(exitCode: 1))
        }
    }
}

final class LauncherPathsTests: XCTestCase {
    func testInstalledPathsUseStablePerUserLocation() {
        let paths = LauncherPaths.installed(
            resourceDirectory: URL(fileURLWithPath: "/bundle/Resources", isDirectory: true),
            homeDirectory: URL(fileURLWithPath: "/Users/alice", isDirectory: true)
        )

        XCTAssertEqual(paths.applicationSupportDirectory.path, "/Users/alice/Library/Application Support/CPACloud")
        XCTAssertEqual(paths.dataDirectory.path, "/Users/alice/Library/Application Support/CPACloud/data")
        XCTAssertEqual(paths.lockFile.path, "/Users/alice/Library/Application Support/CPACloud/launcher.lock")
        XCTAssertEqual(paths.serverExecutable.path, "/bundle/Resources/server/cpa-cloud")
        XCTAssertEqual(paths.webDirectory.path, "/bundle/Resources/web")
    }
}

final class FilesystemSafetyTests: XCTestCase {
    private var temporaryDirectory: URL!

    override func setUpWithError() throws {
        temporaryDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("CPACloudLauncherTests-\(UUID().uuidString)", isDirectory: true)
    }

    override func tearDownWithError() throws {
        if let temporaryDirectory {
            try? FileManager.default.removeItem(at: temporaryDirectory)
        }
    }

    func testSecureDirectoryUsesOwnerOnlyPermissions() throws {
        let dataDirectory = temporaryDirectory.appendingPathComponent("nested/data", isDirectory: true)
        try SecureDirectory.ensure(dataDirectory)
        let attributes = try FileManager.default.attributesOfItem(atPath: dataDirectory.path)
        let permissions = try XCTUnwrap(attributes[.posixPermissions] as? NSNumber)
        XCTAssertEqual(permissions.intValue & 0o777, 0o700)
    }

    func testSingleInstanceLockRejectsSecondOwner() throws {
        let lockFile = temporaryDirectory.appendingPathComponent("launcher.lock")
        let first = try SingleInstanceLock(lockFile: lockFile)
        try withExtendedLifetime(first) {
            XCTAssertThrowsError(try SingleInstanceLock(lockFile: lockFile)) { error in
                XCTAssertEqual(error as? SingleInstanceError, .alreadyRunning)
            }
        }
    }

    func testServerCLITimesOutBoundedOperation() throws {
        try FileManager.default.createDirectory(at: temporaryDirectory, withIntermediateDirectories: true)
        let script = temporaryDirectory.appendingPathComponent("slow-server")
        try Data("#!/bin/sh\nexec /bin/sleep 5\n".utf8).write(to: script, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: script.path)

        let paths = LauncherPaths(applicationSupportDirectory: temporaryDirectory, resourceDirectory: temporaryDirectory)
        let cli = ServerCLI(executable: script, commands: ServerCommands(paths: paths), operationTimeout: 0.1)
        let started = Date()
        XCTAssertThrowsError(try cli.checkInitialization()) { error in
            XCTAssertEqual(error as? ServerCLIError, .timedOut)
        }
        XCTAssertLessThan(Date().timeIntervalSince(started), 2.5)
    }
}
