using System.Diagnostics;

namespace CPACloud.Launcher;

internal static class ServerCommands
{
    internal static ProcessStartInfo CheckInitialized(LauncherPaths paths) =>
        Create(paths, "--check-initialized", "--data-dir", paths.DataDirectory);

    internal static ProcessStartInfo Initialize(LauncherPaths paths) =>
        Create(paths, "--init", "--data-dir", paths.DataDirectory);

    internal static ProcessStartInfo Serve(LauncherPaths paths, string instanceId) =>
        Create(
            paths,
            "--data-dir", paths.DataDirectory,
            "--listen", $"127.0.0.1:{LoopbackPortProbe.ServicePort}",
            "--web-dir", paths.WebDirectory,
            "--instance-id", instanceId,
            "--shutdown-on-stdin-eof");

    private static ProcessStartInfo Create(LauncherPaths paths, params string[] arguments)
    {
        var info = new ProcessStartInfo
        {
            FileName = paths.ServerExecutable,
            WorkingDirectory = paths.InstallDirectory,
            UseShellExecute = false,
            CreateNoWindow = true,
            RedirectStandardInput = true,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
        };

        foreach (var argument in arguments)
        {
            info.ArgumentList.Add(argument);
        }

        return info;
    }
}
