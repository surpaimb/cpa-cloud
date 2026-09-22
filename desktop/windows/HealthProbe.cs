using System.Net;

namespace CPACloud.Launcher;

internal static class HealthProbe
{
    internal static async Task<bool> MatchesAsync(
        HttpClient client,
        Uri endpoint,
        string expectedInstanceId,
        TimeSpan attemptTimeout,
        CancellationToken cancellationToken)
    {
        using var attempt = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        attempt.CancelAfter(attemptTimeout);
        try
        {
            using var response = await client.GetAsync(
                endpoint,
                HttpCompletionOption.ResponseHeadersRead,
                attempt.Token);
            if (response.StatusCode != HttpStatusCode.OK)
            {
                return false;
            }

            return await HealthResponse.ReadAndMatchAsync(
                response.Content,
                expectedInstanceId,
                attempt.Token);
        }
        catch (HttpRequestException)
        {
            return false;
        }
        catch (IOException)
        {
            return false;
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            return false;
        }
    }
}
