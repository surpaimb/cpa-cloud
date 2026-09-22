namespace CPACloud.Launcher;

internal sealed record LauncherPaths(
    string InstallDirectory,
    string ServerExecutable,
    string WebDirectory,
    string DataDirectory)
{
    internal const string RelativeDataDirectory = @"CPACloud\data";

    internal static LauncherPaths ForInstalledApp()
    {
        var localAppData = Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        if (string.IsNullOrWhiteSpace(localAppData))
        {
            throw new InvalidOperationException("Windows 未提供 LOCALAPPDATA 目录。");
        }

        return Create(AppContext.BaseDirectory, localAppData);
    }

    internal static LauncherPaths Create(string installDirectory, string localAppData)
    {
        var install = Path.GetFullPath(installDirectory);
        var data = Path.GetFullPath(Path.Combine(localAppData, RelativeDataDirectory));
        return new LauncherPaths(
            install,
            Path.Combine(install, "cpa-cloud.exe"),
            Path.Combine(install, "web"),
            data);
    }

    internal void ValidateBundle()
    {
        if (!File.Exists(ServerExecutable))
        {
            throw new FileNotFoundException("安装目录中缺少 cpa-cloud.exe。", ServerExecutable);
        }

        if (!Directory.Exists(WebDirectory))
        {
            throw new DirectoryNotFoundException($"安装目录中缺少 web 资源：{WebDirectory}");
        }
    }
}
