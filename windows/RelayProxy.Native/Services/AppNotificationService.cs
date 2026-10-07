using Microsoft.UI.Dispatching;
using Microsoft.Windows.AppNotifications;
using Microsoft.Windows.AppNotifications.Builder;
using RelayProxy.Native.Models;

namespace RelayProxy.Native.Services;

public sealed class AppNotificationService : IDisposable
{
    private readonly object _gate = new();
    private AppNotificationManager? _manager;
    private DispatcherQueue? _dispatcher;
    private Action? _activateWindow;
    private bool _disposed;

    public bool IsRegistered { get; private set; }
    public string LastError { get; private set; } = "";

    public void Initialize(DispatcherQueue dispatcher, Action activateWindow)
    {
        lock (_gate)
        {
            if (_disposed || IsRegistered) return;
            _dispatcher = dispatcher;
            _activateWindow = activateWindow;
            try
            {
                var manager = AppNotificationManager.Default;
                manager.NotificationInvoked += OnNotificationInvoked;
                manager.Register();
                _manager = manager;
                IsRegistered = true;
                LastError = "";
            }
            catch (Exception ex)
            {
                IsRegistered = false;
                LastError = ex.Message;
                _manager = null;
            }
        }
    }

    public bool TryShow(PushMessageDto message)
    {
        lock (_gate)
        {
            if (_disposed || !IsRegistered || _manager is null) return false;
            try
            {
                var title = string.IsNullOrWhiteSpace(message.Title) ? "RelayProxy" : message.Title.Trim();
                var body = string.IsNullOrWhiteSpace(message.Content) ? "收到一条新消息" : message.Content.Trim();
                if (!string.IsNullOrWhiteSpace(message.VerificationCode))
                    body = $"{body}\n验证码：{message.VerificationCode}";

                var notification = new AppNotificationBuilder()
                    .AddArgument("source", "relayproxy-message")
                    .AddArgument("messageId", message.Id ?? "")
                    .AddText(title)
                    .AddText(body)
                    .BuildNotification();
                _manager.Show(notification);
                return true;
            }
            catch (Exception ex)
            {
                LastError = ex.Message;
                return false;
            }
        }
    }

    private void OnNotificationInvoked(AppNotificationManager sender, AppNotificationActivatedEventArgs args)
    {
        _dispatcher?.TryEnqueue(() => _activateWindow?.Invoke());
    }

    public void Dispose()
    {
        lock (_gate)
        {
            if (_disposed) return;
            _disposed = true;
            if (_manager is not null)
            {
                try { _manager.NotificationInvoked -= OnNotificationInvoked; } catch { }
                try { _manager.Unregister(); } catch { }
            }
            _manager = null;
            IsRegistered = false;
        }
    }
}
