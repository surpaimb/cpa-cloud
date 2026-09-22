using System.Net;
using System.Net.Sockets;

namespace CPACloud.Launcher;

internal static class LoopbackPortProbe
{
    internal const int ServicePort = 8787;

    internal static bool IsAvailable(int port = ServicePort)
    {
        TcpListener? listener = null;
        try
        {
            listener = new TcpListener(IPAddress.Loopback, port);
            listener.Server.ExclusiveAddressUse = true;
            listener.Start();
            return true;
        }
        catch (SocketException)
        {
            return false;
        }
        finally
        {
            listener?.Stop();
        }
    }
}
