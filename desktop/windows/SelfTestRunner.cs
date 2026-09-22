using System.Diagnostics;
using System.Net;
using System.Net.Sockets;
using System.Security.AccessControl;
using System.Security.Principal;

namespace CPACloud.Launcher;

internal static class SelfTestRunner
{
    private const string IntegrationPassword = "launcher-integration-password";

    internal static int Run()
    {
        try
        {
            TestPaths();
            TestPasswords();
            TestCommands();
            TestHealthIdentity();
            TestPortProbe();
            TestSecureDataDirectory();
            TestNamedMutexProbe();
            Assert(Program.MutexName == @"Local\CPACloud.Launcher", "single-instance mutex changed");
            Assert(
                Program.SetupMutexName == @"Local\CPACloud.Setup.2D7C1C6E-20A9-46F4-9955-24465BD75DCB",
                "setup mutex changed");
            return 0;
        }
        catch (Exception ex)
        {
            Debug.WriteLine(ex);
            return 1;
        }
    }

    internal static async Task<int> RunIntegrationAsync(string serverExecutable, string webDirectory)
    {
        var testRoot = Path.GetFullPath(Path.Combine(
            Path.GetTempPath(),
            $"CPACloud-launcher-integration-{Guid.NewGuid():N}"));
        var paths = new LauncherPaths(
            Path.GetDirectoryName(Path.GetFullPath(serverExecutable))
                ?? throw new InvalidOperationException("server executable has no parent directory"),
            Path.GetFullPath(serverExecutable),
            Path.GetFullPath(webDirectory),
            Path.Combine(testRoot, "CPACloud", "data"));

        try
        {
            paths.ValidateBundle();
            SecureDataDirectory.EnsureForCurrentUser(paths.DataDirectory);
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(45));

            Assert(await ServerCli.CheckInitializedAsync(paths, timeout.Token) == 3, "integration uninitialized state");
            Assert(await ServerCli.InitializeAsync(paths, IntegrationPassword, timeout.Token) == 0, "integration initialization");
            Assert(await ServerCli.CheckInitializedAsync(paths, timeout.Token) == 0, "integration initialized state");

            using var controller = new ServerProcessController(paths);
            Assert(await controller.StartAsync(timeout.Token), $"integration start: {controller.LastError}");
            Assert(controller.State == ServerState.Running, "integration running state");
            Assert(controller.HasOwnedProcess, "integration owned process");
            await controller.StopAsync();
            Assert(controller.State == ServerState.Stopped, "integration stopped state");
            Assert(!controller.HasOwnedProcess, "integration released process");
            return 0;
        }
        catch (Exception ex)
        {
            Debug.WriteLine(ex);
            return 1;
        }
        finally
        {
            if (Directory.Exists(testRoot))
            {
                Directory.Delete(testRoot, recursive: true);
            }
        }
    }

    private static void TestPaths()
    {
        var paths = LauncherPaths.Create(@"C:\Program Files\CPA Cloud", @"C:\Users\test\AppData\Local");
        Assert(paths.ServerExecutable == @"C:\Program Files\CPA Cloud\cpa-cloud.exe", "server path");
        Assert(paths.WebDirectory == @"C:\Program Files\CPA Cloud\web", "web path");
        Assert(paths.DataDirectory == @"C:\Users\test\AppData\Local\CPACloud\data", "data path");
    }

    private static void TestPasswords()
    {
        Assert(PasswordValidator.Validate("123456789012", "123456789012") is null, "12-byte password");
        Assert(PasswordValidator.Validate(new string('a', 72), new string('a', 72)) is null, "72-byte password");
        Assert(PasswordValidator.Validate("12345678901", "12345678901") is not null, "short password");
        Assert(PasswordValidator.Validate(new string('a', 73), new string('a', 73)) is not null, "long password");
        Assert(PasswordValidator.Validate("密码密码密码密码", "密码密码密码密码") is null, "UTF-8 byte validation");
        Assert(PasswordValidator.Validate("123456789012", "123456789013") is not null, "confirmation");
    }

    private static void TestCommands()
    {
        var paths = LauncherPaths.Create(@"C:\CPA", @"C:\Local");
        var check = ServerCommands.CheckInitialized(paths);
        Assert(check.FileName == @"C:\CPA\cpa-cloud.exe", "absolute executable");
        Assert(check.WorkingDirectory == @"C:\CPA", "absolute working directory");
        Assert(check.ArgumentList.SequenceEqual(["--check-initialized", "--data-dir", paths.DataDirectory]), "check arguments");

        var id = "123e4567-e89b-12d3-a456-426614174000";
        var serve = ServerCommands.Serve(paths, id);
        Assert(serve.ArgumentList.Contains("--shutdown-on-stdin-eof"), "stdin lifecycle flag");
        Assert(serve.ArgumentList.Contains("--instance-id"), "instance flag");
        Assert(serve.ArgumentList.Contains(id), "instance value");
        Assert(serve.RedirectStandardInput && serve.RedirectStandardOutput && serve.RedirectStandardError, "redirected pipes");
        Assert(!serve.UseShellExecute, "no shell");
    }

    private static void TestHealthIdentity()
    {
        const string id = "123e4567-e89b-12d3-a456-426614174000";
        Assert(HealthResponse.Matches($"{{\"status\":\"ok\",\"instance_id\":\"{id}\"}}", id), "matching health");
        Assert(!HealthResponse.Matches("{\"status\":\"ok\"}", id), "missing identity");
        Assert(!HealthResponse.Matches("{\"status\":\"ok\",\"instance_id\":\"other\"}", id), "wrong identity");
        Assert(!HealthResponse.Matches("not-json", id), "invalid health response");
    }

    private static void TestPortProbe()
    {
        using var listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Server.ExclusiveAddressUse = true;
        listener.Start();
        var port = ((IPEndPoint)listener.LocalEndpoint).Port;
        Assert(!LoopbackPortProbe.IsAvailable(port), "occupied port");
        listener.Stop();
        Assert(LoopbackPortProbe.IsAvailable(port), "released port");
    }

    private static void TestSecureDataDirectory()
    {
        var testRoot = Path.GetFullPath(Path.Combine(
            Path.GetTempPath(),
            $"CPACloud-launcher-selftest-{Guid.NewGuid():N}"));
        var dataDirectory = Path.Combine(testRoot, "CPACloud", "data");
        try
        {
            SecureDataDirectory.EnsureForCurrentUser(dataDirectory);
            Assert(Directory.Exists(dataDirectory), "secure data directory creation");

            using var identity = WindowsIdentity.GetCurrent(TokenAccessLevels.Query);
            var currentUser = identity.User ?? throw new InvalidOperationException("missing current user SID");
            var security = new DirectoryInfo(dataDirectory).GetAccessControl(AccessControlSections.Access | AccessControlSections.Owner);
            Assert(security.AreAccessRulesProtected, "ACL inheritance disabled");
            Assert(currentUser.Equals(security.GetOwner(typeof(SecurityIdentifier))), "data directory owner");

            var rules = security.GetAccessRules(includeExplicit: true, includeInherited: true, typeof(SecurityIdentifier))
                .OfType<FileSystemAccessRule>()
                .ToArray();
            Assert(rules.Length == 1, "single data directory access rule");
            Assert(currentUser.Equals(rules[0].IdentityReference), "current-user access rule");
            Assert(rules[0].AccessControlType == AccessControlType.Allow, "allow access rule");
            Assert((rules[0].FileSystemRights & FileSystemRights.FullControl) == FileSystemRights.FullControl, "full-control access rule");
        }
        finally
        {
            if (Directory.Exists(testRoot))
            {
                Directory.Delete(testRoot, recursive: true);
            }
        }
    }

    private static void TestNamedMutexProbe()
    {
        var name = $@"Local\CPACloud.Launcher.SelfTest.{Guid.NewGuid():N}";
        Assert(!Program.IsNamedMutexPresent(name), "absent mutex probe");
        using (new Mutex(initiallyOwned: false, name))
        {
            Assert(Program.IsNamedMutexPresent(name), "present mutex probe");
        }
        Assert(!Program.IsNamedMutexPresent(name), "released mutex probe");
    }

    private static void Assert(bool condition, string name)
    {
        if (!condition)
        {
            throw new InvalidOperationException($"Self-test failed: {name}");
        }
    }
}
