using System.Text;

namespace CPACloud.Launcher;

internal static class PasswordValidator
{
    internal const int MinimumBytes = 12;
    internal const int MaximumBytes = 72;

    internal static string? Validate(string password, string confirmation)
    {
        if (!string.Equals(password, confirmation, StringComparison.Ordinal))
        {
            return "两次输入的密码不一致。";
        }

        var byteCount = Encoding.UTF8.GetByteCount(password);
        if (byteCount < MinimumBytes || byteCount > MaximumBytes)
        {
            return $"密码必须为 {MinimumBytes}–{MaximumBytes} 个 UTF-8 字节（当前 {byteCount} 字节）。";
        }

        return null;
    }
}
