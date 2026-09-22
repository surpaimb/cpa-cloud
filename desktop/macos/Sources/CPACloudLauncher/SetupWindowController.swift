import AppKit
import CPACloudLauncherCore

final class SetupWindowController: NSWindowController, NSWindowDelegate {
    var onInitialize: ((String) -> Void)?
    var onCancel: (() -> Void)?

    private let passwordField = NSSecureTextField()
    private let confirmationField = NSSecureTextField()
    private let errorLabel = NSTextField(labelWithString: "")
    private let initializeButton = NSButton(title: "初始化并启动", target: nil, action: nil)
    private let cancelButton = NSButton(title: "取消", target: nil, action: nil)
    private let progress = NSProgressIndicator()
    private var completed = false

    init(dataDirectory: URL) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 500, height: 370),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "首次设置 CPA Cloud"
        window.isReleasedWhenClosed = false
        window.center()
        super.init(window: window)
        window.delegate = self
        configureContent(dataDirectory: dataDirectory)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not supported")
    }

    func present() {
        completed = false
        setBusy(false)
        errorLabel.stringValue = ""
        NSApp.activate(ignoringOtherApps: true)
        showWindow(nil)
        window?.makeKeyAndOrderFront(nil)
        passwordField.becomeFirstResponder()
    }

    func setBusy(_ busy: Bool, error: String? = nil) {
        passwordField.isEnabled = !busy
        confirmationField.isEnabled = !busy
        initializeButton.isEnabled = !busy
        cancelButton.isEnabled = !busy
        if busy {
            progress.startAnimation(nil)
        } else {
            progress.stopAnimation(nil)
        }
        errorLabel.stringValue = error ?? ""
    }

    func completeAndClose() {
        completed = true
        clearPasswords()
        close()
    }

    func windowWillClose(_ notification: Notification) {
        clearPasswords()
        if !completed {
            onCancel?()
        }
    }

    private func configureContent(dataDirectory: URL) {
        guard let window else { return }

        let root = NSView()
        root.translatesAutoresizingMaskIntoConstraints = false
        window.contentView = root

        let symbol = NSImageView(image: NSImage(systemSymbolName: "cloud.fill", accessibilityDescription: "CPA Cloud") ?? NSImage())
        symbol.contentTintColor = .controlAccentColor
        symbol.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: 34, weight: .medium)

        let title = NSTextField(labelWithString: "设置管理员密码")
        title.font = .systemFont(ofSize: 24, weight: .semibold)
        title.alignment = .center

        let detail = NSTextField(wrappingLabelWithString: "密码仅通过标准输入传给本机 CPA Cloud 服务，不会写入启动器日志。密码长度必须为 12–72 个 UTF-8 字节。")
        detail.textColor = .secondaryLabelColor
        detail.alignment = .center

        let path = NSTextField(wrappingLabelWithString: "数据目录：\(dataDirectory.path)")
        path.font = .systemFont(ofSize: 11)
        path.textColor = .tertiaryLabelColor
        path.alignment = .center

        passwordField.placeholderString = "管理员密码"
        passwordField.setAccessibilityLabel("管理员密码")
        confirmationField.placeholderString = "再次输入密码"
        confirmationField.setAccessibilityLabel("确认管理员密码")

        errorLabel.textColor = .systemRed
        errorLabel.alignment = .center
        errorLabel.maximumNumberOfLines = 2

        progress.style = .spinning
        progress.controlSize = .small
        progress.isDisplayedWhenStopped = false

        initializeButton.bezelStyle = .rounded
        initializeButton.keyEquivalent = "\r"
        initializeButton.target = self
        initializeButton.action = #selector(initializePressed)

        cancelButton.bezelStyle = .rounded
        cancelButton.keyEquivalent = "\u{1b}"
        cancelButton.target = self
        cancelButton.action = #selector(cancelPressed)

        let buttonRow = NSStackView(views: [progress, cancelButton, initializeButton])
        buttonRow.orientation = .horizontal
        buttonRow.alignment = .centerY
        buttonRow.distribution = .gravityAreas
        buttonRow.spacing = 10

        let stack = NSStackView(views: [symbol, title, detail, path, passwordField, confirmationField, errorLabel, buttonRow])
        stack.translatesAutoresizingMaskIntoConstraints = false
        stack.orientation = .vertical
        stack.alignment = .centerX
        stack.spacing = 14
        root.addSubview(stack)

        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: root.leadingAnchor, constant: 38),
            stack.trailingAnchor.constraint(equalTo: root.trailingAnchor, constant: -38),
            stack.topAnchor.constraint(equalTo: root.topAnchor, constant: 28),
            stack.bottomAnchor.constraint(lessThanOrEqualTo: root.bottomAnchor, constant: -24),
            detail.widthAnchor.constraint(equalTo: stack.widthAnchor),
            path.widthAnchor.constraint(equalTo: stack.widthAnchor),
            passwordField.widthAnchor.constraint(equalTo: stack.widthAnchor),
            confirmationField.widthAnchor.constraint(equalTo: stack.widthAnchor),
            errorLabel.widthAnchor.constraint(equalTo: stack.widthAnchor),
            buttonRow.widthAnchor.constraint(equalTo: stack.widthAnchor),
            passwordField.heightAnchor.constraint(equalToConstant: 28),
            confirmationField.heightAnchor.constraint(equalToConstant: 28),
        ])
    }

    @objc private func initializePressed() {
        do {
            try PasswordValidator.validate(passwordField.stringValue, confirmation: confirmationField.stringValue)
            let password = passwordField.stringValue
            clearPasswords()
            setBusy(true)
            onInitialize?(password)
        } catch {
            errorLabel.stringValue = error.localizedDescription
        }
    }

    @objc private func cancelPressed() {
        close()
    }

    private func clearPasswords() {
        passwordField.stringValue = ""
        confirmationField.stringValue = ""
    }
}
