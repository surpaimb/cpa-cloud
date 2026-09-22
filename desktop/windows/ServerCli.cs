using System.Diagnostics;
using System.Security.Cryptography;
using System.Text;

namespace CPACloud.Launcher;

internal static class ServerCli
{
    private static readonly TimeSpan CommandTimeout = TimeSpan.FromSeconds(60);

    internal static Task<int> CheckInitializedAsync(LauncherPaths paths, CancellationToken cancellationToken) =>
        RunAsync(ServerCommands.CheckInitialized(paths), password: null, cancellationToken);

    internal static Task<int> InitializeAsync(
        LauncherPaths paths,
        string password,
        CancellationToken cancellationToken) =>
        RunAsync(ServerCommands.Initialize(paths), password, cancellationToken);

    private static async Task<int> RunAsync(
        ProcessStartInfo startInfo,
        string? password,
        CancellationToken cancellationToken)
    {
        using var process = new Process { StartInfo = startInfo };
        if (!process.Start())
        {
            throw new InvalidOperationException("无法启动 CPA Cloud 服务程序。");
        }

        // Drain both streams so a verbose child cannot block. Output is deliberately discarded.
        var stdout = OutputDrainer.DrainAsync(process.StandardOutput.BaseStream, cancellationToken);
        var stderr = OutputDrainer.DrainAsync(process.StandardError.BaseStream, cancellationToken);

        if (password is null)
        {
            process.StandardInput.Close();
        }
        else
        {
            var bytes = Encoding.UTF8.GetBytes(password);
            try
            {
                await process.StandardInput.BaseStream.WriteAsync(bytes, cancellationToken);
                await process.StandardInput.BaseStream.FlushAsync(cancellationToken);
            }
            finally
            {
                CryptographicOperations.ZeroMemory(bytes);
                process.StandardInput.Close();
            }
        }

        try
        {
            await process.WaitForExitAsync(cancellationToken).WaitAsync(CommandTimeout, cancellationToken);
            await Task.WhenAll(stdout, stderr);
            return process.ExitCode;
        }
        catch
        {
            TryKill(process);
            throw;
        }
    }

    private static void TryKill(Process process)
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
}
