using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;
using RelayProxy.Native.Models;
using Windows.ApplicationModel.DataTransfer;
using Windows.Graphics;

namespace RelayProxy.Native;

public sealed partial class MessagePopupWindow : Window
{
    private readonly Queue<(PushMessageDto Message, int Timeout)> _queue = new();
    private DispatcherTimer? _timer;
    private PushMessageDto? _message;
    private bool _showing;

    public MessagePopupWindow()
    {
        InitializeComponent();
        Title = "RelayProxy 消息";
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(PopupTitleBar);
        SystemBackdrop = new MicaBackdrop();
        AppWindow.Resize(new SizeInt32(500, 330));
        if (AppWindow.Presenter is OverlappedPresenter presenter)
        {
            presenter.IsResizable = false;
            presenter.IsMaximizable = false;
            presenter.IsMinimizable = false;
            presenter.IsAlwaysOnTop = true;
        }
    }

    public void EnqueueMessage(PushMessageDto message, int timeoutSeconds)
    {
        if (_message?.Id == message.Id || _queue.Any(x => x.Message.Id == message.Id)) return;
        _queue.Enqueue((message, timeoutSeconds));
        if (!_showing) ShowNext();
    }

    public static bool ShouldPopup(PushMessageDto message)
    {
        if (string.IsNullOrWhiteSpace(message.MessageType) && string.IsNullOrWhiteSpace(message.PopupType))
            return message.Popup || !string.IsNullOrWhiteSpace(message.VerificationCode);
        return message.Popup;
    }

    private void ShowNext()
    {
        _timer?.Stop();
        if (_queue.Count == 0)
        {
            _showing = false;
            _message = null;
            Hide();
            return;
        }

        _showing = true;
        var item = _queue.Dequeue();
        _message = item.Message;
        var type = PopupType(item.Message);
        TypeText.Text = type switch { "verification_code" => "验证码", "important" => "重要提醒", _ => "消息" };
        TypeIconText.Text = type switch { "verification_code" => "123", "important" => "!", _ => "✦" };
        TitleText.Text = string.IsNullOrWhiteSpace(item.Message.Title) ? "RelayProxy 消息" : item.Message.Title;
        ContentText.Text = item.Message.Content;
        MetaText.Text = string.Join(" · ", new[] { item.Message.Source, item.Message.MessageRule }.Where(x => !string.IsNullOrWhiteSpace(x)));
        CodePanel.Visibility = string.IsNullOrWhiteSpace(item.Message.VerificationCode) ? Visibility.Collapsed : Visibility.Visible;
        CopyButton.Visibility = CodePanel.Visibility;
        CodeText.Text = item.Message.VerificationCode;

        if (item.Timeout > 0)
        {
            _timer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(Math.Clamp(item.Timeout, 1, 3600)) };
            _timer.Tick += (_, _) => CompleteCurrent();
            _timer.Start();
        }
        Activate();
    }

    private static string PopupType(PushMessageDto message)
    {
        if (!string.IsNullOrWhiteSpace(message.MessageType)) return message.MessageType;
        if (!string.IsNullOrWhiteSpace(message.PopupType)) return message.PopupType;
        if (!string.IsNullOrWhiteSpace(message.VerificationCode)) return "verification_code";
        return "message";
    }

    private void CompleteCurrent()
    {
        _timer?.Stop();
        _message = null;
        _showing = false;
        ShowNext();
    }

    private void Copy_Click(object sender, RoutedEventArgs e)
    {
        if (_message is null || string.IsNullOrWhiteSpace(_message.VerificationCode)) return;
        var data = new DataPackage();
        data.SetText(_message.VerificationCode);
        Clipboard.SetContent(data);
        CompleteCurrent();
    }

    private void Close_Click(object sender, RoutedEventArgs e) => CompleteCurrent();
}
