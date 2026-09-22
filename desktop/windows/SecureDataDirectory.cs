using System.Security.AccessControl;
using System.Security.Principal;

namespace CPACloud.Launcher;

internal static class SecureDataDirectory
{
    internal static void EnsureForCurrentUser(string dataDirectory)
    {
        if (!OperatingSystem.IsWindows())
        {
            throw new PlatformNotSupportedException("CPA Cloud Windows 启动器只能在 Windows 上运行。");
        }

        var root = Directory.GetParent(dataDirectory)?.FullName
            ?? throw new InvalidOperationException("无法确定 CPA Cloud 数据根目录。");

        Directory.CreateDirectory(root);
        ApplyCurrentUserOnlyAcl(root);
        Directory.CreateDirectory(dataDirectory);
        ApplyCurrentUserOnlyAcl(dataDirectory);
    }

    private static void ApplyCurrentUserOnlyAcl(string path)
    {
        using var identity = WindowsIdentity.GetCurrent(TokenAccessLevels.Query);
        var user = identity.User
            ?? throw new InvalidOperationException("无法确定当前 Windows 用户 SID。");

        var security = new DirectorySecurity();
        security.SetAccessRuleProtection(isProtected: true, preserveInheritance: false);
        security.SetOwner(user);
        security.AddAccessRule(new FileSystemAccessRule(
            user,
            FileSystemRights.FullControl,
            InheritanceFlags.ContainerInherit | InheritanceFlags.ObjectInherit,
            PropagationFlags.None,
            AccessControlType.Allow));

        new DirectoryInfo(path).SetAccessControl(security);
    }
}
