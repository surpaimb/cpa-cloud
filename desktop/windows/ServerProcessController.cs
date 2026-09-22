using System.Diagnostics;
using System.Net;

namespace CPACloud.Launcher;

internal enum ServerState
{
    Stopped,
    Starting,
    Running,
    Stopping,
    Error,
}

internal sealed class ServerProcessController : IDisposable
{
    private static readonly TimeSpan ReadinessTimeout = TimeSpan.FromSeconds(20);
    internal static readonly TimeSpan HealthAttemptTimeout = TimeSpan.FromSeconds(2);
    private static readonly TimeSpan StopTimeout = TimeSpan.FromSeconds(10);
    private readonly object _gate = new();
    private readonly LauncherPaths _paths;
    private readonly HttpClient _httpClient;
    private Process? _process;
    private bool _intentionalStop;
    private bool _disposed;
    private ServerState _state = ServerState.Stopped;
    private string? _lastError;

    internal ServerProcessController(LauncherPaths paths)
    {
        _paths = paths;
        _httpClient = new HttpClient(CreateHttpHandler())
        {
            Timeout = Timeout.InfiniteTimeSpan,
        };
    }

    internal static SocketsHttpHandler CreateHttpHandler() => new()
    {
        UseProxy = false,
        AllowAutoRedirect = false,
        AutomaticDecompression = DecompressionMethods.None,
        MaxResponseHeadersLength = 4,
    };

    internal event EventHandler? StateChanged;

    internal ServerState State
    {
        get
        {
            lock (_gate)
            {
                return _state;
            }
        }
    }

    internal string? LastError
    {
        get
        {
            lock (_gate)
            {
                return _lastError;
            }
        }
    }

    internal bool HasOwnedProcess
    {
        get
        {
            lock (_gate)
            {
                return _process is not null;
            }
        }
    }

    internal async Task<bool> StartAsync(CancellationToken cancellationToken)
    {
        ThrowIfDisposed();
        lock (_gate)
        {
            if (_process is not null || _state is ServerState.Starting or ServerState.Running or ServerState.Stopping)
            {
                return false;
            }

            _state = ServerState.Starting;
            _lastError = null;
        }
        RaiseStateChanged();

        if (!LoopbackPortProbe.IsAvailable())
        {
            SetError("127.0.0.1:8787 已被其他程序占用。未启动服务，也未终止占用者。");
            return false;
        }

        var instanceId = Guid.NewGuid().ToString("D").ToLowerInvariant();
        var process = new Process
        {
            StartInfo = ServerCommands.Serve(_paths, instanceId),
            EnableRaisingEvents = true,
        };
        process.Exited += OwnedProcessExited;

        lock (_gate)
        {
            _process = process;
            _intentionalStop = false;
        }

        try
        {
            if (!process.Start())
            {
                throw new InvalidOperationException("Windows 未能启动服务进程。");
            }

            _ = OutputDrainer.DrainAsync(process.StandardOutput.BaseStream);
            _ = OutputDrainer.DrainAsync(process.StandardError.BaseStream);
        }
        catch (Exception ex)
        {
            ClearOwnedProcess(process);
            process.Dispose();
            SetError($"服务进程启动失败：{ex.Message}");
            return false;
        }

        var deadline = DateTime.UtcNow + ReadinessTimeout;
        while (DateTime.UtcNow < deadline)
        {
            cancellationToken.ThrowIfCancellationRequested();
            if (HasExited(process))
            {
                return false;
            }

            if (await HasMatchingHealthAsync(instanceId, cancellationToken))
            {
                lock (_gate)
                {
                    if (!ReferenceEquals(_process, process) || HasExited(process))
                    {
                        return false;
                    }

                    _state = ServerState.Running;
                    _lastError = null;
                }
                RaiseStateChanged();
                return true;
            }

            await Task.Delay(200, cancellationToken);
        }

        await StopSpecificProcessAsync(process);
        SetError("服务启动后未在限定时间内通过身份匹配的健康检查。");
        return false;
    }

    internal async Task StopAsync()
    {
        Process? process;
        lock (_gate)
        {
            process = _process;
            if (process is null)
            {
                _state = ServerState.Stopped;
                _lastError = null;
                return;
            }

            _intentionalStop = true;
            _state = ServerState.Stopping;
            _lastError = null;
        }
        RaiseStateChanged();

        await StopSpecificProcessAsync(process);

        lock (_gate)
        {
            if (ReferenceEquals(_process, process))
            {
                _process = null;
            }
            _state = ServerState.Stopped;
            _lastError = null;
        }
        process.Dispose();
        RaiseStateChanged();
    }

    private async Task<bool> HasMatchingHealthAsync(string instanceId, CancellationToken cancellationToken)
    {
        return await HealthProbe.MatchesAsync(
            _httpClient,
            new Uri($"http://127.0.0.1:{LoopbackPortProbe.ServicePort}/healthz"),
            instanceId,
            HealthAttemptTimeout,
            cancellationToken);
    }

    private async Task StopSpecificProcessAsync(Process process)
    {
        try
        {
            if (!HasExited(process))
            {
                process.StandardInput.Close();
            }
        }
        catch (InvalidOperationException)
        {
        }

        try
        {
            if (!HasExited(process))
            {
                await process.WaitForExitAsync().WaitAsync(StopTimeout);
            }
        }
        catch (TimeoutException)
        {
            TryKillOwnedProcess(process);
            try
            {
                await process.WaitForExitAsync().WaitAsync(TimeSpan.FromSeconds(3));
            }
            catch (TimeoutException)
            {
            }
        }
        catch (InvalidOperationException)
        {
        }
    }

    private void OwnedProcessExited(object? sender, EventArgs args)
    {
        if (sender is not Process process)
        {
            return;
        }

        lock (_gate)
        {
            if (!ReferenceEquals(_process, process))
            {
                return;
            }

            // A dead child is no longer owned state. Clearing it lets the user
            // explicitly start again, but there is intentionally no auto-restart.
            _process = null;

            if (_intentionalStop)
            {
                _state = ServerState.Stopped;
                _lastError = null;
            }
            else
            {
                _state = ServerState.Error;
                _lastError = $"服务意外退出（退出码 {SafeExitCode(process)}）。不会自动重启。";
            }
        }
        RaiseStateChanged();
    }

    private void ClearOwnedProcess(Process process)
    {
        lock (_gate)
        {
            if (ReferenceEquals(_process, process))
            {
                _process = null;
            }
        }
    }

    private void SetError(string message)
    {
        lock (_gate)
        {
            _state = ServerState.Error;
            _lastError = message;
        }
        RaiseStateChanged();
    }

    private static bool HasExited(Process process)
    {
        try
        {
            return process.HasExited;
        }
        catch (InvalidOperationException)
        {
            return true;
        }
    }

    private static int SafeExitCode(Process process)
    {
        try
        {
            return process.ExitCode;
        }
        catch (InvalidOperationException)
        {
            return -1;
        }
    }

    private static void TryKillOwnedProcess(Process process)
    {
        try
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
            }
        }
        catch (InvalidOperationException)
        {
        }
        catch (System.ComponentModel.Win32Exception)
        {
        }
    }

    private void RaiseStateChanged() => StateChanged?.Invoke(this, EventArgs.Empty);

    private void ThrowIfDisposed()
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
    }

    public void Dispose()
    {
        if (_disposed)
        {
            return;
        }

        StopAsync().GetAwaiter().GetResult();
        _httpClient.Dispose();
        _disposed = true;
    }
}
