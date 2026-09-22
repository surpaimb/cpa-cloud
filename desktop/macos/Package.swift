// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "CPACloudLauncher",
    platforms: [
        .macOS(.v13),
    ],
    products: [
        .library(name: "CPACloudLauncherCore", targets: ["CPACloudLauncherCore"]),
        .executable(name: "CPACloudLauncher", targets: ["CPACloudLauncher"]),
    ],
    targets: [
        .target(
            name: "CPACloudLauncherShim",
            publicHeadersPath: "include"
        ),
        .target(
            name: "CPACloudLauncherCore",
            dependencies: ["CPACloudLauncherShim"]
        ),
        .executableTarget(
            name: "CPACloudLauncher",
            dependencies: ["CPACloudLauncherCore"]
        ),
        .testTarget(
            name: "CPACloudLauncherCoreTests",
            dependencies: ["CPACloudLauncherCore"]
        ),
    ]
)
