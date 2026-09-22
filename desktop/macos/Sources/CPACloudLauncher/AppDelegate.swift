import AppKit
import CPACloudLauncherCore

final class AppDelegate: NSObject, NSApplicationDelegate {
    private static let bundleIdentifier = "com.surpaimb.cpa-cloud.launcher"
    private static let openDashboardNotification = Notification.Name("com.surpaimb.cpa-cloud.launcher.open-dashboard")

    private var instanceLock: SingleInstanceLock?
    private var paths: LauncherPaths?
    private var serverCLI: ServerCLI?
    private var serviceController: ServerProcessController?
    private var setupWindowController: SetupWindowController?
    private var statusItem: NSStatusItem?
    private let statusMenuItem = NSMenuItem(title: "状态：正在检查", action: nil, keyEquivalent: "")
    private let openMenuItem = NSMenuItem(title: "打开后台", action: nil, keyEquivalent: "o")
    private let startMenuItem = NSMenuItem(title: "启动服务", action: nil, keyEquivalent: "s")
    private let stopMenuItem = NSMenuItem(title: "停止服务", action: nil, keyEquivalent: "")
    private var terminationPending = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)

        guard let resourceDirectory = Bundle.main.resourceURL else {
            showFatalAndTerminate("应用资源目录不可用。")
            return
        }

        let paths = LauncherPaths.installed(resourceDirectory: resourceDirectory)
        self.paths = paths

        do {
            instanceLock = try SingleInstanceLock(lockFile: paths.lockFile)
        } catch SingleInstanceError.alreadyRunning {
            notifyExistingInstance()
            NSApp.terminate(nil)
            return
        } catch {
            showFatalAndTerminate(error.localizedDescription)
            return
        }

        configureMenu()
        DistributedNotificationCenter.default().addObserver(
            self,
            selector: #selector(existingInstanceWasOpened),
            name: Self.openDashboardNotification,
            object: nil
        )

        do {
            try paths.validatePackagedResources()
            try SecureDirectory.ensure(paths.applicationSupportDirectory)
        } catch {
            updateStatus(.failed(error.localizedDescription))
            showAlert(title: "CPA Cloud 无法启动", message: error.localizedDescription)
            return
        }

        let cli = ServerCLI(executable: paths.serverExecutable, commands: ServerCommands(paths: paths))
        serverCLI = cli
        let controller = ServerProcessController(paths: paths)
        controller.onStateChange = { [weak self] state in self?.updateStatus(state) }
        serviceController = controller
        checkInitialization(thenStart: true)
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if terminationPending {
            return .terminateLater
        }
        guard serviceController?.ownsRunningService == true else {
            return .terminateNow
        }
        terminationPending = true
        serviceController?.stop {
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationWillTerminate(_ notification: Notification) {
        DistributedNotificationCenter.default().removeObserver(self)
    }

    private func configureMenu() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.button?.image = NSImage(systemSymbolName: "cloud.fill", accessibilityDescription: "CPA Cloud")
        item.button?.toolTip = "CPA Cloud"

        let menu = NSMenu()
        statusMenuItem.isEnabled = false
        menu.addItem(statusMenuItem)
        menu.addItem(.separator())

        openMenuItem.target = self
        openMenuItem.action = #selector(openDashboard)
        menu.addItem(openMenuItem)
        startMenuItem.target = self
        startMenuItem.action = #selector(startServiceFromMenu)
        menu.addItem(startMenuItem)
        stopMenuItem.target = self
        stopMenuItem.action = #selector(stopServiceFromMenu)
        menu.addItem(stopMenuItem)
        menu.addItem(.separator())

        let quit = NSMenuItem(title: "退出 CPA Cloud", action: #selector(quitApplication), keyEquivalent: "q")
        quit.target = self
        menu.addItem(quit)
        item.menu = menu
        statusItem = item
        updateStatus(.stopped)
    }

    private func checkInitialization(thenStart: Bool) {
        guard let cli = serverCLI else { return }
        statusMenuItem.title = "状态：正在检查"
        startMenuItem.isEnabled = false
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try cli.checkInitialization() }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(.initialized):
                    self.updateStatus(.stopped)
                    if thenStart { self.startServiceAndOpenDashboard() }
                case .success(.notInitialized):
                    self.updateStatus(.stopped)
                    self.presentSetupWindow()
                case .failure(let error):
                    self.updateStatus(.failed(error.localizedDescription))
                    self.showAlert(title: "无法检查数据目录", message: error.localizedDescription)
                }
            }
        }
    }

    private func presentSetupWindow() {
        guard let paths else { return }
        if setupWindowController == nil {
            let controller = SetupWindowController(dataDirectory: paths.dataDirectory)
            controller.onInitialize = { [weak self] password in self?.initialize(password: password) }
            setupWindowController = controller
        }
        setupWindowController?.present()
    }

    private func initialize(password: String) {
        guard let cli = serverCLI, let paths else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try SecureDirectory.ensure(paths.dataDirectory)
                try cli.initialize(password: password)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success:
                    self.setupWindowController?.completeAndClose()
                    self.startServiceAndOpenDashboard()
                case .failure(let error):
                    self.setupWindowController?.setBusy(false, error: error.localizedDescription)
                }
            }
        }
    }

    private func startServiceAndOpenDashboard() {
        guard let serviceController else { return }
        do {
            try serviceController.start { [weak self] in self?.openDashboardWhenReady() }
        } catch {
            updateStatus(.failed(error.localizedDescription))
            showAlert(title: "服务启动失败", message: error.localizedDescription)
        }
    }

    private func openDashboardWhenReady() {
        guard serviceController?.isReady == true else { return }
        NSWorkspace.shared.open(URL(string: "http://127.0.0.1:8787")!)
    }

    private func updateStatus(_ state: ServiceState) {
        statusMenuItem.toolTip = nil
        switch state {
        case .stopped:
            statusMenuItem.title = "状态：已停止"
            openMenuItem.isEnabled = false
            startMenuItem.isEnabled = true
            stopMenuItem.isEnabled = false
        case .starting:
            statusMenuItem.title = "状态：正在启动"
            openMenuItem.isEnabled = false
            startMenuItem.isEnabled = false
            stopMenuItem.isEnabled = true
        case .running:
            statusMenuItem.title = "状态：运行中"
            openMenuItem.isEnabled = true
            startMenuItem.isEnabled = false
            stopMenuItem.isEnabled = true
        case .stopping:
            statusMenuItem.title = "状态：正在停止"
            openMenuItem.isEnabled = false
            startMenuItem.isEnabled = false
            stopMenuItem.isEnabled = false
        case .failed(let reason):
            statusMenuItem.title = "状态：错误"
            statusMenuItem.toolTip = reason
            openMenuItem.isEnabled = false
            startMenuItem.isEnabled = true
            stopMenuItem.isEnabled = false
        }
    }

    @objc private func openDashboard() {
        guard serviceController?.isReady == true else {
            startServiceFromMenu()
            return
        }
        openDashboardWhenReady()
    }

    @objc private func startServiceFromMenu() {
        checkInitialization(thenStart: true)
    }

    @objc private func stopServiceFromMenu() {
        serviceController?.stop {}
    }

    @objc private func quitApplication() {
        NSApp.terminate(nil)
    }

    @objc private func existingInstanceWasOpened() {
        NSApp.activate(ignoringOtherApps: true)
        if setupWindowController?.window?.isVisible == true {
            setupWindowController?.window?.makeKeyAndOrderFront(nil)
        } else if serviceController?.isReady == true {
            openDashboardWhenReady()
        } else {
            startServiceFromMenu()
        }
    }

    private func notifyExistingInstance() {
        DistributedNotificationCenter.default().post(name: Self.openDashboardNotification, object: nil)
        NSRunningApplication.runningApplications(withBundleIdentifier: Self.bundleIdentifier)
            .first(where: { $0.processIdentifier != ProcessInfo.processInfo.processIdentifier })?
            .activate(options: [.activateIgnoringOtherApps])
    }

    private func showFatalAndTerminate(_ message: String) {
        showAlert(title: "CPA Cloud 启动器错误", message: message)
        NSApp.terminate(nil)
    }

    private func showAlert(title: String, message: String) {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: "好")
        alert.runModal()
    }
}
