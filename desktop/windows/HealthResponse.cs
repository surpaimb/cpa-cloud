using System.Text.Json;

namespace CPACloud.Launcher;

internal static class HealthResponse
{
    internal static bool Matches(string json, string expectedInstanceId)
    {
        try
        {
            using var document = JsonDocument.Parse(json);
            var root = document.RootElement;
            return root.ValueKind == JsonValueKind.Object
                && root.TryGetProperty("status", out var status)
                && string.Equals(status.GetString(), "ok", StringComparison.Ordinal)
                && root.TryGetProperty("instance_id", out var instanceId)
                && string.Equals(instanceId.GetString(), expectedInstanceId, StringComparison.Ordinal);
        }
        catch (JsonException)
        {
            return false;
        }
    }
}
