using System.Diagnostics;

namespace CPACloud.Launcher;

internal sealed class LauncherApplicationContext : ApplicationContext
{
    private const string ConsoleUrl = "http://127.0.0.1:8787/";
    private readonly LauncherPaths _paths;
    private readonly ServerProcessController _server;
    private readonly NotifyIcon _notifyIcon;
    private readonly ToolStripMenuItem _statusItem;
    private readonly ToolStripMenuItem _openItem;
    private readonly ToolStripMenuItem _startItem;
    private readonly ToolStripMenuItem _stopItem;
    private readonly Control _dispatcher = new();
    private readonly SemaphoreSlim _operation = new(1, 1);
    private readonly CancellationTokenSource _lifetime = new();
    private readonly System.Windows.Forms.Timer _startupTimer;
    private bool _exiting;
    private string? _operationStatus;

    internal LauncherApplicationContext(LauncherPaths paths)
    {
        _paths = paths;
        _paths.ValidateBundle();
        SecureDataDirectory.EnsureForCurrentUser(_paths.DataDirectory);

        _dispatcher.CreateControl();
        _server = new ServerProcessController(paths);
        _server.StateChanged += ServerStateChanged;

        _statusItem = new ToolStripMenuItem("状态：已停止") { Enabled = false };
        _openItem = new ToolStripMenuItem("打开后台", null, (_, _) => OpenConsole());
        _startItem = new ToolStripMenuItem("启动服务", null, async (_, _) => await StartFromMenuAsync());
        _stopItem = new ToolStripMenuItem("停止服务", null, async (_, _) => await StopFromMenuAsync());
        var exitItem = new ToolStripMenuItem("退出", null, async (_, _) => await ExitAsync());

        var menu = new ContextMenuStrip();
        menu.Items.AddRange([
            _statusItem,
            new ToolStripSeparator(),
            _openItem,
            _startItem,
            _stopItem,
            new ToolStripSeparator(),
            exitItem,
        ]);

        _notifyIcon = new NotifyIcon
        {
            Icon = SystemIcons.Application,
            Text = "CPA Cloud - 已停止",
            ContextMenuStrip = menu,
            Visible = true,
        };
        _notifyIcon.DoubleClick += (_, _) => OpenConsole();

        UpdateMenu();
        _startupTimer = new System.Windows.Forms.Timer { Interval = 1 };
        _startupTimer.Tick += async (_, _) =>
        {
            _startupTimer.Stop();
            await StartFromMenuAsync();
        };
        _startupTimer.Start();
    }

    private async Task StartFromMenuAsync()
    {
        if (_exiting || !await _operation.WaitAsync(0))
        {
            return;
        }

        try
        {
            SetOperationStatus("检查初始化状态…");
            var checkCode = await ServerCli.CheckInitializedAsync(_paths, _lifetime.Token);
            if (checkCode == 3)
            {
                SetOperationStatus("等待设置管理员密码");
                using var dialog = new PasswordDialog();
                if (dialog.ShowDialog() != DialogResult.OK)
                {
                    SetOperationStatus(null);
                    return;
                }

                SetOperationStatus("正在初始化…");
                var initCode = await ServerCli.InitializeAsync(_paths, dialog.Password, _lifetime.Token);
                if (initCode != 0)
                {
                    SetOperationStatus(null);
                    ShowError("初始化失败。数据未被视为可用，您可以从托盘菜单重试。");
                    return;
                }

                checkCode = await ServerCli.CheckInitializedAsync(_paths, _lifetime.Token);
            }

            if (checkCode != 0)
            {
                SetOperationStatus(null);
                ShowError("数据目录无法通过初始化检查。请检查磁盘权限或数据是否损坏。");
                return;
            }

            SetOperationStatus(null);
            var ready = await _server.StartAsync(_lifetime.Token);
            if (ready)
            {
                OpenConsole();
            }
            else if (_server.LastError is { } error)
            {
                ShowError(error);
            }
        }
        catch (OperationCanceledException) when (_exiting)
        {
        }
        catch (Exception ex)
        {
            SetOperationStatus(null);
            ShowError($"启动服务失败：{ex.Message}");
        }
        finally
        {
            _operation.Release();
            UpdateMenu();
        }
    }

    private async Task StopFromMenuAsync()
    {
        if (_exiting || !await _operation.WaitAsync(0))
        {
            return;
        }

        try
        {
            await _server.StopAsync();
        }
        catch (Exception ex)
        {
            ShowError($"停止服务时发生错误：{ex.Message}");
        }
        finally
        {
            _operation.Release();
            UpdateMenu();
        }
    }

    private async Task ExitAsync()
    {
        if (_exiting)
        {
            return;
        }

        _exiting = true;
        _lifetime.Cancel();
        _startupTimer.Stop();
        SetOperationStatus("正在退出…");

        await _operation.WaitAsync();
        try
        {
            await _server.StopAsync();
        }
        finally
        {
            _operation.Release();
        }

        _notifyIcon.Visible = false;
        ExitThread();
    }

    private void OpenConsole()
    {
        if (_server.State != ServerState.Running)
        {
            return;
        }

        try
        {
            Process.Start(new ProcessStartInfo(ConsoleUrl) { UseShellExecute = true });
        }
        catch (Exception ex)
        {
            ShowError($"无法打开默认浏览器：{ex.Message}");
        }
    }

    private void ServerStateChanged(object? sender, EventArgs args)
    {
        if (_dispatcher.IsDisposed || !_dispatcher.IsHandleCreated)
        {
            return;
        }

        try
        {
            _dispatcher.BeginInvoke(UpdateMenu);
        }
        catch (InvalidOperationException)
        {
        }
    }

    private void SetOperationStatus(string? status)
    {
        _operationStatus = status;
        UpdateMenu();
    }

    private void UpdateMenu()
    {
        if (_dispatcher.InvokeRequired)
        {
            _dispatcher.BeginInvoke(UpdateMenu);
            return;
        }

        var state = _server.State;
        var text = _operationStatus ?? state switch
        {
            ServerState.Stopped => "已停止",
            ServerState.Starting => "正在启动…",
            ServerState.Running => "运行中",
            ServerState.Stopping => "正在停止…",
            ServerState.Error => "错误/已停止",
            _ => "未知",
        };

        _statusItem.Text = $"状态：{text}";
        _notifyIcon.Text = TruncateTooltip($"CPA Cloud - {text}");
        var busy = _operation.CurrentCount == 0 || _operationStatus is not null;
        _openItem.Enabled = !busy && state == ServerState.Running;
        _startItem.Enabled = !busy
            && state is (ServerState.Stopped or ServerState.Error)
            && !_server.HasOwnedProcess;
        _stopItem.Enabled = !busy && _server.HasOwnedProcess;
    }

    private void ShowError(string message)
    {
        MessageBox.Show(message, "CPA Cloud", MessageBoxButtons.OK, MessageBoxIcon.Error);
    }

    private static string TruncateTooltip(string value) => value.Length <= 63 ? value : value[..63];

    protected override void ExitThreadCore()
    {
        _startupTimer.Dispose();
        _notifyIcon.Visible = false;
        _notifyIcon.Dispose();
        _server.StateChanged -= ServerStateChanged;
        _server.Dispose();
        _lifetime.Dispose();
        _operation.Dispose();
        _dispatcher.Dispose();
        base.ExitThreadCore();
    }
}
