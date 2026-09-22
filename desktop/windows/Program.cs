namespace CPACloud.Launcher;

internal static class Program
{
    internal const string MutexName = @"Local\CPACloud.Launcher";
    internal const string SetupMutexName = @"Local\CPACloud.Setup.2D7C1C6E-20A9-46F4-9955-24465BD75DCB";

    [STAThread]
    private static int Main(string[] args)
    {
        if (args.Length == 1 && args[0] == "--self-test")
        {
            return SelfTestRunner.Run();
        }

        if (args.Length == 3 && args[0] == "--integration-test")
        {
            return SelfTestRunner.RunIntegrationAsync(args[1], args[2]).GetAwaiter().GetResult();
        }

        ApplicationConfiguration.Initialize();

        if (IsNamedMutexPresent(SetupMutexName))
        {
            MessageBox.Show(
                "CPA Cloud 正在安装或卸载，请完成后重试。\n\nCPA Cloud setup is in progress. Please try again when it finishes.",
                "CPA Cloud",
                MessageBoxButtons.OK,
                MessageBoxIcon.Information);
            return 0;
        }

        using var mutex = new Mutex(initiallyOwned: true, MutexName, out var createdNew);
        if (!createdNew)
        {
            MessageBox.Show(
                "CPA Cloud 已在运行。请使用通知区域中的 CPA Cloud 图标打开后台。",
                "CPA Cloud",
                MessageBoxButtons.OK,
                MessageBoxIcon.Information);
            return 0;
        }

        try
        {
            using var context = new LauncherApplicationContext(LauncherPaths.ForInstalledApp());
            Application.Run(context);
            return 0;
        }
        catch (Exception ex)
        {
            MessageBox.Show(
                $"CPA Cloud 启动器无法启动。\n\n{ex.Message}",
                "CPA Cloud",
                MessageBoxButtons.OK,
                MessageBoxIcon.Error);
            return 1;
        }
        finally
        {
            try
            {
                mutex.ReleaseMutex();
            }
            catch (ApplicationException)
            {
                // The mutex is process-scoped and may already have been released.
            }
        }
    }

    internal static bool IsNamedMutexPresent(string name)
    {
        try
        {
            if (!Mutex.TryOpenExisting(name, out var setupMutex))
            {
                return false;
            }

            setupMutex.Dispose();
            return true;
        }
        catch (UnauthorizedAccessException)
        {
            // Treat an inaccessible mutex with the exact setup name as active.
            return true;
        }
    }
}
