using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Media;
using RelayProxy.Native.Models;
using Windows.ApplicationModel.DataTransfer;
using Windows.Graphics;

namespace RelayProxy.Native;

public sealed partial class MessagePopupWindow : Window
{
    private DispatcherTimer? _timer;
    private PushMessageDto? _message;

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

    public void ShowMessage(PushMessageDto message, int timeoutSeconds)
    {
        _message = message;
        var type = PopupType(message);
        TypeText.Text = type switch { "verification_code" => "验证码", "important" => "重要提醒", _ => "消息" };
        TypeIconText.Text = type switch { "verification_code" => "123", "important" => "!", _ => "✦" };
        TitleText.Text = string.IsNullOrWhiteSpace(message.Title) ? "RelayProxy 消息" : message.Title;
        ContentText.Text = message.Content;
        MetaText.Text = string.Join(" · ", new[] { message.Source, message.MessageRule }.Where(x => !string.IsNullOrWhiteSpace(x)));
        CodePanel.Visibility = string.IsNullOrWhiteSpace(message.VerificationCode) ? Visibility.Collapsed : Visibility.Visible;
        CopyButton.Visibility = CodePanel.Visibility;
        CodeText.Text = message.VerificationCode;

        _timer?.Stop();
        if (timeoutSeconds > 0)
        {
            _timer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(Math.Clamp(timeoutSeconds, 1, 3600)) };
            _timer.Tick += (_, _) => { _timer?.Stop(); Hide(); };
            _timer.Start();
        }

        Activate();
    }

    public static bool ShouldPopup(PushMessageDto message)
    {
        if (string.IsNullOrWhiteSpace(message.MessageType) && string.IsNullOrWhiteSpace(message.PopupType))
            return message.Popup || !string.IsNullOrWhiteSpace(message.VerificationCode);
        return message.Popup;
    }

    private static string PopupType(PushMessageDto message)
    {
        if (!string.IsNullOrWhiteSpace(message.MessageType)) return message.MessageType;
        if (!string.IsNullOrWhiteSpace(message.PopupType)) return message.PopupType;
        if (!string.IsNullOrWhiteSpace(message.VerificationCode)) return "verification_code";
        return "message";
    }

    private void Copy_Click(object sender, RoutedEventArgs e)
    {
        if (_message is null || string.IsNullOrWhiteSpace(_message.VerificationCode)) return;
        var data = new DataPackage();
        data.SetText(_message.VerificationCode);
        Clipboard.SetContent(data);
        Hide();
    }

    private void Close_Click(object sender, RoutedEventArgs e)
    {
        _timer?.Stop();
        Hide();
    }
}
