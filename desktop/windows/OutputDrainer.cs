namespace CPACloud.Launcher;

internal static class OutputDrainer
{
    internal const int BufferSize = 8192;

    internal static async Task DrainAsync(Stream stream, CancellationToken cancellationToken = default)
    {
        var buffer = new byte[BufferSize];
        try
        {
            while (await stream.ReadAsync(buffer, cancellationToken) != 0)
            {
                // Output is intentionally discarded and never accumulated.
            }
        }
        catch (IOException)
        {
        }
        catch (ObjectDisposedException)
        {
        }
        catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
        {
        }
    }
}
