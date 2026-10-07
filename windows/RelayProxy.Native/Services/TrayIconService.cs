using System.Runtime.InteropServices;
using Microsoft.UI.Xaml;

namespace RelayProxy.Native.Services;

public sealed class TrayIconService : IDisposable
{
    private const int GwlpWndProc = -4;
    private const uint NimAdd = 0x00000000;
    private const uint NimDelete = 0x00000002;
    private const uint NifMessage = 0x00000001;
    private const uint NifIcon = 0x00000002;
    private const uint NifTip = 0x00000004;
    private const uint WmApp = 0x8000;
    private const uint CallbackMessage = WmApp + 42;
    private const uint WmLButtonUp = 0x0202;
    private const uint WmLButtonDblClk = 0x0203;
    private const uint WmRButtonUp = 0x0205;
    private const uint WmContextMenu = 0x007B;
    private const uint MfString = 0x0000;
    private const uint MfSeparator = 0x0800;
    private const uint TpmRightButton = 0x0002;
    private const uint TpmReturnCmd = 0x0100;
    private const uint TpmNoNotify = 0x0080;
    private const uint ImageIcon = 1;
    private const uint LrLoadFromFile = 0x0010;
    private const uint LrDefaultSize = 0x0040;

    private readonly Window _window;
    private readonly nint _hwnd;
    private readonly WndProc _wndProc;
    private readonly nint _oldWndProc;
    private NotifyIconData _data;
    private bool _disposed;

    public event Action? ShowRequested;
    public event Action? CopyDeviceIdRequested;
    public event Action? ExitRequested;

    public TrayIconService(Window window, string? iconPath)
    {
        _window = window;
        _hwnd = WinRT.Interop.WindowNative.GetWindowHandle(window);
        _wndProc = WindowProc;
        _oldWndProc = SetWindowLongPtr(_hwnd, GwlpWndProc, Marshal.GetFunctionPointerForDelegate(_wndProc));

        var icon = !string.IsNullOrWhiteSpace(iconPath) && File.Exists(iconPath)
            ? LoadImage(0, iconPath, ImageIcon, 0, 0, LrLoadFromFile | LrDefaultSize)
            : 0;
        if (icon == 0) icon = LoadIcon(0, (nint)32512);

        _data = new NotifyIconData
        {
            cbSize = (uint)Marshal.SizeOf<NotifyIconData>(),
            hWnd = _hwnd,
            uID = 1,
            uFlags = NifMessage | NifIcon | NifTip,
            uCallbackMessage = CallbackMessage,
            hIcon = icon,
            szTip = "RelayProxy"
        };
        ShellNotifyIcon(NimAdd, ref _data);
    }

    public void UpdateTooltip(string value)
    {
        if (_disposed) return;
        _data.szTip = string.IsNullOrWhiteSpace(value) ? "RelayProxy" : value[..Math.Min(value.Length, 127)];
        const uint nimModify = 0x00000001;
        ShellNotifyIcon(nimModify, ref _data);
    }

    private nint WindowProc(nint hwnd, uint msg, nint wParam, nint lParam)
    {
        if (msg == CallbackMessage)
        {
            var code = (uint)(lParam.ToInt64() & 0xffff);
            if (code is WmLButtonUp or WmLButtonDblClk)
            {
                ShowRequested?.Invoke();
                return 0;
            }
            if (code is WmRButtonUp or WmContextMenu)
            {
                ShowContextMenu();
                return 0;
            }
        }
        return CallWindowProc(_oldWndProc, hwnd, msg, wParam, lParam);
    }

    private void ShowContextMenu()
    {
        if (!GetCursorPos(out var point)) return;
        var menu = CreatePopupMenu();
        if (menu == 0) return;
        try
        {
            AppendMenu(menu, MfString, 1, "打开 RelayProxy");
            AppendMenu(menu, MfString, 2, "复制设备 ID");
            AppendMenu(menu, MfSeparator, 0, null);
            AppendMenu(menu, MfString, 3, "退出");
            SetForegroundWindow(_hwnd);
            var command = TrackPopupMenu(menu, TpmRightButton | TpmReturnCmd | TpmNoNotify, point.X, point.Y, 0, _hwnd, 0);
            if (command == 1) ShowRequested?.Invoke();
            else if (command == 2) CopyDeviceIdRequested?.Invoke();
            else if (command == 3) ExitRequested?.Invoke();
        }
        finally { DestroyMenu(menu); }
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        ShellNotifyIcon(NimDelete, ref _data);
        if (_oldWndProc != 0) SetWindowLongPtr(_hwnd, GwlpWndProc, _oldWndProc);
        GC.KeepAlive(_wndProc);
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct NotifyIconData
    {
        public uint cbSize;
        public nint hWnd;
        public uint uID;
        public uint uFlags;
        public uint uCallbackMessage;
        public nint hIcon;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 128)] public string szTip;
        public uint dwState;
        public uint dwStateMask;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 256)] public string szInfo;
        public uint uTimeoutOrVersion;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 64)] public string szInfoTitle;
        public uint dwInfoFlags;
        public Guid guidItem;
        public nint hBalloonIcon;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct Point { public int X; public int Y; }

    [UnmanagedFunctionPointer(CallingConvention.Winapi)]
    private delegate nint WndProc(nint hwnd, uint msg, nint wParam, nint lParam);

    [DllImport("shell32.dll", CharSet = CharSet.Unicode, EntryPoint = "Shell_NotifyIconW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool ShellNotifyIcon(uint message, ref NotifyIconData data);

    [DllImport("user32.dll", CharSet = CharSet.Unicode, EntryPoint = "LoadImageW", SetLastError = true)]
    private static extern nint LoadImage(nint instance, string name, uint type, int cx, int cy, uint load);

    [DllImport("user32.dll", EntryPoint = "LoadIconW")]
    private static extern nint LoadIcon(nint instance, nint iconName);

    [DllImport("user32.dll", EntryPoint = "SetWindowLongPtrW", SetLastError = true)]
    private static extern nint SetWindowLongPtr(nint hwnd, int index, nint value);

    [DllImport("user32.dll", EntryPoint = "CallWindowProcW")]
    private static extern nint CallWindowProc(nint previous, nint hwnd, uint msg, nint wParam, nint lParam);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern nint CreatePopupMenu();

    [DllImport("user32.dll", CharSet = CharSet.Unicode, EntryPoint = "AppendMenuW")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool AppendMenu(nint menu, uint flags, nuint id, string? text);

    [DllImport("user32.dll", SetLastError = true)]
    private static extern uint TrackPopupMenu(nint menu, uint flags, int x, int y, int reserved, nint hwnd, nint rect);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool DestroyMenu(nint menu);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetCursorPos(out Point point);

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetForegroundWindow(nint hwnd);
}
