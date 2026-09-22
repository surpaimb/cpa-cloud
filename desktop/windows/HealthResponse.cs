using System.Text.Json;
using System.Text;

namespace CPACloud.Launcher;

internal static class HealthResponse
{
    internal const int MaximumBodyBytes = 4096;

    internal static async Task<bool> ReadAndMatchAsync(
        HttpContent content,
        string expectedInstanceId,
        CancellationToken cancellationToken)
    {
        if (content.Headers.ContentLength is > MaximumBodyBytes)
        {
            return false;
        }

        await using var stream = await content.ReadAsStreamAsync(cancellationToken);
        var buffer = new byte[MaximumBodyBytes + 1];
        var total = 0;
        while (total < buffer.Length)
        {
            var read = await stream.ReadAsync(buffer.AsMemory(total, buffer.Length - total), cancellationToken);
            if (read == 0)
            {
                break;
            }
            total += read;
        }

        if (total > MaximumBodyBytes)
        {
            return false;
        }

        return Matches(Encoding.UTF8.GetString(buffer, 0, total), expectedInstanceId);
    }

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
