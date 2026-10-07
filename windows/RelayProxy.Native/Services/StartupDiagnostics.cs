using System.Runtime.InteropServices;
using System.Text;

namespace RelayProxy.Native.Services;

internal static class StartupDiagnostics
{
    private static readonly object Gate = new();
    private static readonly string DirectoryPath = Path.Combine(Path.GetTempPath(), "RelayProxy");
    public static string LogPath { get; } = Path.Combine(DirectoryPath, "startup.log");

    public static void Write(string message, Exception? exception = null)
    {
        try
        {
            lock (Gate)
            {
                Directory.CreateDirectory(DirectoryPath);
                var builder = new StringBuilder()
                    .Append(DateTimeOffset.Now.ToString("O"))
                    .Append("  ")
                    .AppendLine(message);
                if (exception is not null)
                    builder.AppendLine(exception.ToString());
                File.AppendAllText(LogPath, builder.ToString(), Encoding.UTF8);
            }
        }
        catch
        {
            // Startup logging must never prevent the app from launching.
        }
    }

    public static void ShowFatal(string message, Exception exception)
    {
        try
        {
            var detail = $"{message}\n\n{exception.Message}\n\n日志：{LogPath}";
            MessageBoxW(IntPtr.Zero, detail, "RelayProxy", 0x00000010);
        }
        catch
        {
            // If User32 is unavailable there is nothing else useful to do here.
        }
    }

    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern int MessageBoxW(IntPtr hWnd, string text, string caption, uint type);
}
