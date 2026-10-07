using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using RelayProxy.Native.Models;
using Windows.ApplicationModel.DataTransfer;
using Windows.Graphics;

namespace RelayProxy.Native;

public sealed partial class MainWindow : Window
{
    private readonly DispatcherTimer _timer = new() { Interval = TimeSpan.FromSeconds(2) };
    private string _page = "overview";

    public MainWindow()
    {
        InitializeComponent();
        Title = "RelayProxy";
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(AppTitleBar);
        SystemBackdrop = new MicaBackdrop();
        AppWindow.Resize(new SizeInt32(1180, 780));
        App.AgentHost.StateChanged += OnAgentStateChanged;
        Closed += OnClosed;
        _timer.Tick += async (_, _) => await RefreshCurrentAsync();
        _timer.Start();
        _ = RefreshOverviewAsync();
    }

    private void OnAgentStateChanged(string state) => DispatcherQueue.TryEnqueue(() =>
    {
        AgentStateText.Text = state;
        SettingsAgentStateText.Text = state;
        ManagementUrlText.Text = App.AgentApi.BaseAddress?.ToString() ?? "尚未连接";
    });

    private void OnClosed(object sender, WindowEventArgs args)
    {
        _timer.Stop();
        App.AgentHost.StateChanged -= OnAgentStateChanged;
        _ = App.AgentHost.StopAsync();
    }

    private void Navigation_SelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        if (args.SelectedItemContainer?.Tag is string tag) ShowPage(tag);
    }

    private void ShowPage(string tag)
    {
        _page = tag;
        OverviewView.Visibility = tag == "overview" ? Visibility.Visible : Visibility.Collapsed;
        ExitsView.Visibility = tag == "exits" ? Visibility.Visible : Visibility.Collapsed;
        ConnectionsView.Visibility = tag == "connections" ? Visibility.Visible : Visibility.Collapsed;
        MessagesView.Visibility = tag == "messages" ? Visibility.Visible : Visibility.Collapsed;
        SettingsView.Visibility = tag == "settings" ? Visibility.Visible : Visibility.Collapsed;
        PlaceholderView.Visibility = new[] { "overview", "exits", "connections", "messages", "settings" }.Contains(tag) ? Visibility.Collapsed : Visibility.Visible;
        if (PlaceholderView.Visibility == Visibility.Visible)
        {
            (PlaceholderTitle.Text, PlaceholderDescription.Text) = tag switch
            {
                "devices" => ("身份与设备", "设备审批、能力与身份隔离页面将直接读取 Agent 与服务端授权状态。"),
                "connection" => ("连接与路径", "Server、Identity、QUIC/TLS、Public Direct、P2P 与 Relay 回退策略。"),
                "proxy" => ("本机代理", "SOCKS5、HTTP 与 Windows 系统透明代理服务。"),
                "exitshare" => ("本机出口共享", "Exit 开关、互联网/私网/回环权限、上游代理与 ACL。"),
                "routing" => ("分流规则", "模式、默认动作、规则编辑/排序、进程匹配与原生数据报要求。"),
                "rdp" => ("远程桌面", "仅显示服务端授权设备；P2P 优先，失败回退 Relay。"),
                "diagnostics" => ("诊断与日志", "控制连接、P2P/Public Direct、透明代理、连接监控与诊断采集。"),
                _ => ("模块", "正在迁移到 WinUI 3 原生实现。")
            };
        }
        _ = RefreshCurrentAsync();
    }

    private async Task RefreshCurrentAsync()
    {
        if (_page == "overview") await RefreshOverviewAsync();
        else if (_page == "exits") await RefreshExitsAsync();
        else if (_page == "connections") await RefreshConnectionsAsync();
        else if (_page == "messages") await RefreshMessagesAsync();
    }

    private async Task RefreshOverviewAsync()
    {
        try
        {
            var status = await App.AgentApi.GetStatusAsync();
            var traffic = await App.AgentApi.GetConnectionsAsync();
            if (status is null) return;
            OverviewBar.IsOpen = false;
            ConnectionStateText.Text = status.Connected ? "已连接" : "未连接";
            ConnectionMetaText.Text = status.Connected ? $"{status.Transport.ToUpperInvariant()} · {status.DeviceName} · {status.IdentityName}" : "正在连接 Relay Server";
            ApprovalText.Text = status.ApprovalState switch { "approved" => "已审批", "pending" => "待审批", "rejected" => "已拒绝", "revoked" => "已撤销", _ => "未知" };
            LatencyText.Text = status.LatencyMs > 0 ? $"{status.LatencyMs} ms" : "—";
            SocksStateText.Text = status.Socks5Running ? "运行中" : "已停止";
            HttpStateText.Text = status.HttpRunning ? "运行中" : "已停止";
            ExitStateText.Text = status.ExitRunning ? "运行中" : "已停止";
            ActiveStreamsText.Text = status.ActiveStreams.ToString();
            var selected = status.ProxyExits.FirstOrDefault(x => x.DeviceId == status.SelectedExit);
            ExitNameText.Text = selected?.Name ?? (string.IsNullOrWhiteSpace(status.SelectedExit) ? "自动选择" : status.SelectedExit);
            ExitMetaText.Text = selected is null ? "等待授权出口状态" : $"{(selected.Online ? "在线" : "离线")} · {selected.AuthorizationSource}";
            DirectStateText.Text = StateLabel(status.DirectState);
            DirectDetailText.Text = string.IsNullOrWhiteSpace(status.DirectPath) ? "未建立" : $"{status.DirectPath} · {status.DirectRttMs} ms";
            P2PStateText.Text = StateLabel(status.P2PState);
            P2PDetailText.Text = string.IsNullOrWhiteSpace(status.P2PPath) ? "备用路径" : $"{status.P2PPath} · {status.P2PRttMs} ms";
            DownloadRateText.Text = FormatRate(traffic?.DownloadRate ?? 0);
            UploadRateText.Text = FormatRate(traffic?.UploadRate ?? 0);
        }
        catch (Exception ex)
        {
            OverviewBar.Message = ex.Message;
            OverviewBar.IsOpen = true;
        }
    }

    private async Task RefreshExitsAsync()
    {
        try
        {
            var exits = await App.AgentApi.GetExitsAsync();
            ExitsPanel.Children.Clear();
            foreach (var exit in exits.OrderByDescending(x => x.Online).ThenBy(x => x.Name))
            {
                var text = exit.Direct?.Public?.Available == true ? $"Public Direct · {exit.Direct.Public.Transport.ToUpperInvariant()}" : "P2P / Relay";
                var button = new Button { Content = "设为默认", Tag = exit.DeviceId, VerticalAlignment = VerticalAlignment.Center };
                button.Click += async (_, _) => { try { await App.AgentApi.SelectExitAsync(exit.DeviceId); await RefreshExitsAsync(); } catch (Exception ex) { ShowInfo(ExitsBar, "切换失败", ex.Message, InfoBarSeverity.Error); } };
                var grid = new Grid { ColumnSpacing = 14 };
                grid.ColumnDefinitions.Add(new ColumnDefinition()); grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                var stack = new StackPanel { Spacing = 3 };
                stack.Children.Add(new TextBlock { Text = string.IsNullOrWhiteSpace(exit.Name) ? exit.DeviceId : exit.Name, FontSize = 15, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
                stack.Children.Add(new TextBlock { Text = $"{(exit.Online ? "在线" : "离线")} · {text} · {exit.IdentityName}", Foreground = ThemeBrush("TextFillColorSecondaryBrush") });
                grid.Children.Add(stack); Grid.SetColumn(button, 1); grid.Children.Add(button);
                ExitsPanel.Children.Add(Card(grid));
            }
            ExitsBar.IsOpen = exits.Count == 0;
            if (exits.Count == 0) ShowInfo(ExitsBar, "暂无授权出口", "请确认当前身份已审批设备，并已授权可用出口。", InfoBarSeverity.Informational);
        }
        catch (Exception ex) { ShowInfo(ExitsBar, "正在等待服务端出口", ex.Message, InfoBarSeverity.Warning); }
    }

    private async Task RefreshConnectionsAsync()
    {
        try
        {
            var snapshot = await App.AgentApi.GetConnectionsAsync(); if (snapshot is null) return;
            ActiveConnectionsText.Text = snapshot.Active.ToString(); ConnectionsDownText.Text = FormatRate(snapshot.DownloadRate); ConnectionsUpText.Text = FormatRate(snapshot.UploadRate);
            ConnectionsPanel.Children.Clear();
            foreach (var c in snapshot.Connections.OrderByDescending(x => x.Id).Take(120))
            {
                var target = $"{(!string.IsNullOrWhiteSpace(c.Host) ? c.Host : c.Ip)}:{c.Port}";
                var process = !string.IsNullOrWhiteSpace(c.ProcessName) ? c.ProcessName : (!string.IsNullOrWhiteSpace(c.Process) ? Path.GetFileName(c.Process) : "未知进程");
                var grid = new Grid { ColumnSpacing = 12 };
                for (var i = 0; i < 5; i++) grid.ColumnDefinitions.Add(new ColumnDefinition { Width = i < 2 ? new GridLength(1, GridUnitType.Star) : GridLength.Auto });
                grid.Children.Add(TwoLine(process, string.IsNullOrWhiteSpace(c.Rule) ? $"{c.Action} · {c.Protocol.ToUpperInvariant()}" : c.Rule));
                var dest = TwoLine(target, string.IsNullOrWhiteSpace(c.Path) ? c.ExitId : $"{c.Path} · {c.ExitId}"); Grid.SetColumn(dest, 1); grid.Children.Add(dest);
                var down = new TextBlock { Text = FormatRate(c.DownloadRate), VerticalAlignment = VerticalAlignment.Center }; Grid.SetColumn(down, 2); grid.Children.Add(down);
                var up = new TextBlock { Text = FormatRate(c.UploadRate), VerticalAlignment = VerticalAlignment.Center }; Grid.SetColumn(up, 3); grid.Children.Add(up);
                var state = new TextBlock { Text = c.State, VerticalAlignment = VerticalAlignment.Center }; Grid.SetColumn(state, 4); grid.Children.Add(state);
                ConnectionsPanel.Children.Add(Card(grid));
            }
        }
        catch { }
    }

    private async Task RefreshMessagesAsync()
    {
        try
        {
            var messages = await App.AgentApi.GetMessagesAsync(); MessagesPanel.Children.Clear(); MessagesBar.IsOpen = false;
            foreach (var m in messages.OrderByDescending(x => x.CreatedAt).Take(200))
            {
                var grid = new Grid { ColumnSpacing = 12 };
                grid.ColumnDefinitions.Add(new ColumnDefinition()); grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                var type = m.PopupType switch { "important" => "重要提醒", "verification_code" => "验证码", "message" => "普通消息", _ => string.IsNullOrWhiteSpace(m.VerificationCode) ? "普通消息" : "验证码" };
                var when = m.CreatedAt > 0 ? DateTimeOffset.FromUnixTimeMilliseconds(m.CreatedAt).ToLocalTime().ToString("g") : "";
                grid.Children.Add(TwoLine($"{type} · {(string.IsNullOrWhiteSpace(m.Title) ? "RelayProxy 消息" : m.Title)}", $"{m.Content}\n{string.Join(" · ", new[] { m.Source, m.MessageRule, when }.Where(x => !string.IsNullOrWhiteSpace(x)))}"));
                if (!string.IsNullOrWhiteSpace(m.VerificationCode))
                {
                    var button = new Button { Content = $"复制 {m.VerificationCode}", Tag = m.VerificationCode, VerticalAlignment = VerticalAlignment.Center };
                    button.Click += (_, _) => { var p = new DataPackage(); p.SetText((string)button.Tag); Clipboard.SetContent(p); ShowInfo(MessagesBar, "验证码已复制", (string)button.Tag, InfoBarSeverity.Success); };
                    Grid.SetColumn(button, 1); grid.Children.Add(button);
                }
                MessagesPanel.Children.Add(Card(grid));
            }
        }
        catch (Exception ex) { ShowInfo(MessagesBar, "消息接口尚未就绪", ex.Message, InfoBarSeverity.Warning); }
    }

    private async void RefreshOverview_Click(object sender, RoutedEventArgs e) => await RefreshOverviewAsync();
    private async void RefreshExits_Click(object sender, RoutedEventArgs e) => await RefreshExitsAsync();
    private async void RefreshConnections_Click(object sender, RoutedEventArgs e) => await RefreshConnectionsAsync();
    private async void RefreshMessages_Click(object sender, RoutedEventArgs e) => await RefreshMessagesAsync();
    private async void ClearMessages_Click(object sender, RoutedEventArgs e) { try { await App.AgentApi.ClearMessagesAsync(); await RefreshMessagesAsync(); ShowInfo(MessagesBar, "消息历史已清空", "", InfoBarSeverity.Success); } catch (Exception ex) { ShowInfo(MessagesBar, "清空失败", ex.Message, InfoBarSeverity.Error); } }
    private void OpenExits_Click(object sender, RoutedEventArgs e) => SelectNavigation("exits");

    private void SelectNavigation(string tag)
    {
        var item = Navigation.MenuItems.OfType<NavigationViewItem>().FirstOrDefault(x => (x.Tag as string) == tag);
        if (item is not null) Navigation.SelectedItem = item;
        ShowPage(tag);
    }

    private static Border Card(UIElement child) => new() { Style = (Style)Application.Current.Resources["CardStyle"], Child = child };
    private static StackPanel TwoLine(string title, string subtitle)
    {
        var s = new StackPanel { Spacing = 2 };
        s.Children.Add(new TextBlock { Text = title, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        s.Children.Add(new TextBlock { Text = subtitle, Foreground = ThemeBrush("TextFillColorSecondaryBrush"), TextWrapping = TextWrapping.Wrap });
        return s;
    }
    private static Brush ThemeBrush(string key) => (Brush)Application.Current.Resources[key];
    private static void ShowInfo(InfoBar bar, string title, string message, InfoBarSeverity severity) { bar.Title = title; bar.Message = message; bar.Severity = severity; bar.IsOpen = true; }
    private static string StateLabel(string value) => value switch { "connected" or "active" or "direct" => "已直连", "connecting" or "negotiating" or "punching" => "协商中", "cooldown" => "冷却中", "fallback" => "已降级", "disabled" => "已关闭", "unavailable" => "不可用", _ => string.IsNullOrWhiteSpace(value) ? "—" : value };
    private static string FormatRate(double n) => n < 1024 ? $"{n:0} B/s" : n < 1024 * 1024 ? $"{n / 1024:0.0} KB/s" : $"{n / 1024 / 1024:0.0} MB/s";
}