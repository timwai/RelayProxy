using System.Diagnostics;
using System.IO.Compression;
using System.Text.Json;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using RelayProxy.Native.Models;
using RelayProxy.Native.Services;
using Windows.ApplicationModel.DataTransfer;
using Windows.Graphics;

namespace RelayProxy.Native;

public sealed partial class MainWindow : Window
{
    private readonly DispatcherTimer _timer = new() { Interval = TimeSpan.FromSeconds(2) };
    private readonly HashSet<string> _seenMessageIds = new(StringComparer.Ordinal);
    private readonly HashSet<string> _speedTestingExits = new(StringComparer.Ordinal);
    private readonly Dictionary<string, SpeedTestResultDto> _speedTests = new(StringComparer.Ordinal);
    private readonly HashSet<string> _dirtyPages = new(StringComparer.Ordinal);
    private readonly ServerConsoleClient _serverConsole = new();
    private List<ConnectionDto> _connectionCache = [];
    private List<MessageChannelDto> _serverChannels = [];
    private MessageChannelDto? _selectedMessageChannel;
    private List<MessageRuleDto> _messageRulesDraft = [];
    private bool _suppressMessageChannelSelection;
    private List<LogEntryDto> _logCache = [];
    private List<PushMessageDto> _messageCache = [];
    private AgentConfigDto? _config;
    private List<RoutingRuleDto> _routingRules = [];
    private MessagePopupWindow? _popupWindow;
    private TrayIconService? _tray;
    private bool _messageBaselineReady;
    private bool _suppressAutostart;
    private bool _forceExit;
    private bool _shutdownInProgress;
    private bool _diagnosticCollecting;
    private CancellationTokenSource? _rdpConnectCts;
    private bool _suppressDirtyTracking;
    private bool _suppressNavigationSelection;
    private string _lastDeviceId = "";
    private string _lastApprovalState = "";
    private string _lastExitIssueKey = "";
    private string _page = "overview";
    private bool _proxyPaused;

    public MainWindow()
    {
        InitializeComponent();
        RegisterDirtyTracking();
        Title = "RelayProxy";
        ExtendsContentIntoTitleBar = true;
        SetTitleBar(AppTitleBar);
        SystemBackdrop = new MicaBackdrop();
        AppWindow.Resize(new SizeInt32(1180, 780));
        var iconPath = Path.Combine(AppContext.BaseDirectory, "Assets", "RelayProxy.ico");
        if (File.Exists(iconPath)) AppWindow.SetIcon(iconPath);
        _tray = new TrayIconService(this, iconPath);
        _tray.ShowRequested += OnTrayShowRequested;
        _tray.CopyDeviceIdRequested += OnTrayCopyDeviceIdRequested;
        _tray.ToggleProxyPauseRequested += OnTrayToggleProxyPauseRequested;
        _tray.ExitRequested += OnTrayExitRequested;
        AppWindow.Closing += OnAppWindowClosing;
        AppWindow.Changed += OnAppWindowChanged;
        if (Content is FrameworkElement root)
            root.Loaded += (_, _) => UpdateTitleBarInset();
        App.AgentHost.StateChanged += OnAgentStateChanged;
        Closed += OnClosed;
        _timer.Tick += async (_, _) => await RefreshCurrentAsync();
        _timer.Start();
        _ = RefreshOverviewAsync();
    }

    private void OnAgentStateChanged(string state) => DispatcherQueue.TryEnqueue(() =>
    {
        AgentStateText.Text = state;
        UpdateAgentStateVisual(state);
        SettingsAgentStateText.Text = state;
        ManagementUrlText.Text = App.AgentApi.BaseAddress?.ToString() ?? "尚未连接";
        _tray?.UpdateTooltip($"RelayProxy · {state}");
        if (App.AgentApi.IsReady) _ = LoadConfigAsync();
    });

    private void OnClosed(object sender, WindowEventArgs args)
    {
        _timer.Stop();
        try { _rdpConnectCts?.Cancel(); } catch { }
        _rdpConnectCts?.Dispose();
        _rdpConnectCts = null;
        App.AgentHost.StateChanged -= OnAgentStateChanged;
        AppWindow.Closing -= OnAppWindowClosing;
        AppWindow.Changed -= OnAppWindowChanged;
        try { _popupWindow?.ClosePermanently(); } catch { }
        App.Notifications.Dispose();
        _serverConsole.Dispose();
        if (_tray is not null)
        {
            _tray.ShowRequested -= OnTrayShowRequested;
            _tray.CopyDeviceIdRequested -= OnTrayCopyDeviceIdRequested;
            _tray.ToggleProxyPauseRequested -= OnTrayToggleProxyPauseRequested;
            _tray.ExitRequested -= OnTrayExitRequested;
            _tray.Dispose();
        }
        _tray = null;
    }

    private void OnAppWindowChanged(AppWindow sender, AppWindowChangedEventArgs args) => UpdateTitleBarInset();

    private void UpdateTitleBarInset()
    {
        if (!AppWindowTitleBar.IsCustomizationSupported()) return;
        var scale = (Content as FrameworkElement)?.XamlRoot?.RasterizationScale ?? 1.0;
        if (scale <= 0) scale = 1.0;
        TitleBarRightInsetSpacer.Width = Math.Max(0, AppWindow.TitleBar.RightInset / scale);
    }

    private void UpdateAgentStateVisual(string state)
    {
        var key = state switch
        {
            var s when s.Contains("正常", StringComparison.Ordinal) || s.Contains("已连接", StringComparison.Ordinal) => "SystemFillColorSuccessBrush",
            var s when s.Contains("正在", StringComparison.Ordinal) || s.Contains("启动", StringComparison.Ordinal) => "SystemFillColorCautionBrush",
            _ => "SystemFillColorCriticalBrush",
        };
        AgentStateDot.Fill = ThemeBrush(key);
    }

    private void OnAppWindowClosing(AppWindow sender, AppWindowClosingEventArgs args)
    {
        if (_forceExit) return;
        args.Cancel = true;

        if (_config?.MinimizeToTray ?? true)
        {
            sender.Hide();
            return;
        }

        _ = RequestShutdownAsync();
    }

    private async Task RequestShutdownAsync()
    {
        if (!await ConfirmDiscardChangesAsync("退出 RelayProxy")) return;
        _dirtyPages.Clear();
        await ShutdownAndCloseAsync();
    }

    private async Task ShutdownAndCloseAsync()
    {
        if (_shutdownInProgress) return;
        _shutdownInProgress = true;
        try
        {
            await App.AgentHost.StopAsync();
        }
        finally
        {
            _forceExit = true;
            Close();
        }
    }

    public void ShowFromExternalActivation()
    {
        AppWindow.Show();
        Activate();
    }

    private void OnTrayShowRequested() => DispatcherQueue.TryEnqueue(ShowFromExternalActivation);

    private void OnTrayCopyDeviceIdRequested() => DispatcherQueue.TryEnqueue(() =>
    {
        if (string.IsNullOrWhiteSpace(_lastDeviceId)) return;
        var data = new DataPackage();
        data.SetText(_lastDeviceId);
        Clipboard.SetContent(data);
    });

    private void OnTrayToggleProxyPauseRequested() => DispatcherQueue.TryEnqueue(() => _ = SetProxyPausedAsync(!_proxyPaused, showFeedback: false));

    private void OnTrayExitRequested() => DispatcherQueue.TryEnqueue(() => _ = RequestShutdownAsync());

    private async void Navigation_SelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        if (_suppressNavigationSelection) return;
        if (args.SelectedItemContainer?.Tag is not string tag || tag == _page) return;

        if (_dirtyPages.Contains(_page))
        {
            if (!await ConfirmDiscardChangesAsync("切换页面"))
            {
                RestoreNavigationSelection(_page);
                return;
            }
            _dirtyPages.Remove(_page);
            if (_page == "messages")
                RestoreMessageRuleDraft();
            else
                await LoadConfigAsync();
        }
        ShowPage(tag);
    }

    private void ShowPage(string tag)
    {
        _page = tag;
        var map = new Dictionary<string, UIElement>
        {
            ["overview"] = OverviewView, ["devices"] = DevicesView, ["connection"] = ConnectionView, ["exits"] = ExitsView,
            ["proxy"] = ProxyView, ["exitshare"] = ExitShareView, ["routing"] = RoutingView, ["rdp"] = RdpView,
            ["connections"] = ConnectionsView, ["messages"] = MessagesView, ["diagnostics"] = DiagnosticsView, ["settings"] = SettingsView,
        };
        foreach (var view in map.Values) view.Visibility = Visibility.Collapsed;
        if (map.TryGetValue(tag, out var selected))
            selected.Visibility = Visibility.Visible;
        else
        {
            _page = "overview";
            OverviewView.Visibility = Visibility.Visible;
        }

        if (tag is "connection" or "proxy" or "exitshare" or "routing" or "settings") _ = LoadConfigAsync();
        _ = RefreshCurrentAsync();
    }

    private async Task RefreshCurrentAsync()
    {
        if (_page == "overview") await RefreshOverviewAsync();
        else if (_page == "exits") await RefreshExitsAsync();
        else if (_page == "connections") await RefreshConnectionsAsync();
        else if (_page == "messages") await RefreshMessagesAsync();
        else if (_page == "rdp") await RefreshRdpAsync();
        else if (_page == "devices") await RefreshDevicesAsync();
        else if (_page == "diagnostics") await RefreshDiagnosticsAsync();
        else if (_page == "proxy") await RefreshNetworkServiceAsync();
        else await PollMessagesAsync(render: false);
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
            _proxyPaused = status.ProxyPaused;
            PauseProxyButton.Content = status.ProxyPaused ? "恢复代理" : "暂停代理";
            PauseProxyButton.IsEnabled = status.Connected;
            ConnectionMetaText.Text = status.Connected
                ? $"{status.Transport.ToUpperInvariant()} · {status.DeviceName} · {status.IdentityName}{(status.ProxyPaused ? " · 代理已暂停" : "")}"
                : "正在连接 Relay Server";
            ApprovalText.Text = status.ApprovalState switch { "approved" => "已审批", "pending" => "待审批", "rejected" => "已拒绝", "revoked" => "已撤销", _ => "未知" };
            _lastDeviceId = status.DeviceId;
            if (!string.Equals(_lastApprovalState, status.ApprovalState, StringComparison.OrdinalIgnoreCase))
            {
                _lastApprovalState = status.ApprovalState;
                if (status.ApprovalState is "pending" or "rejected" or "revoked")
                    await ShowApprovalStateDialogAsync(status);
            }
            LatencyText.Text = status.LatencyMs > 0 ? $"{status.LatencyMs} ms" : "—";
            SocksStateText.Text = status.Socks5Running ? "运行中" : "已停止";
            HttpStateText.Text = status.HttpRunning ? "运行中" : "已停止";
            ExitStateText.Text = status.ExitRunning ? "运行中" : "已停止";
            ActiveStreamsText.Text = status.ActiveStreams.ToString();
            var selected = status.ProxyExits.FirstOrDefault(x => x.DeviceId == status.SelectedExit);
            ExitNameText.Text = selected?.Name ?? (string.IsNullOrWhiteSpace(status.SelectedExit) ? "自动选择" : status.SelectedExit);
            ExitMetaText.Text = selected is null ? "等待授权出口状态" : $"{(selected.Online ? "在线" : "离线")} · {selected.AuthorizationSource}";

            var exitIssue = string.IsNullOrWhiteSpace(status.SelectedExit)
                ? ""
                : selected is null
                    ? $"missing:{status.SelectedExit}"
                    : selected.Online ? "" : $"offline:{status.SelectedExit}";
            if (exitIssue != _lastExitIssueKey)
            {
                _lastExitIssueKey = exitIssue;
                if (!string.IsNullOrWhiteSpace(exitIssue))
                    await ShowExitUnavailableDialogAsync(status.SelectedExit, selected?.Name, selected is null);
            }
            DirectStateText.Text = StateLabel(status.DirectState);
            DirectDetailText.Text = string.IsNullOrWhiteSpace(status.DirectPath) ? "未建立" : $"{status.DirectPath} · {status.DirectRttMs} ms";
            P2PStateText.Text = StateLabel(status.P2PState);
            P2PDetailText.Text = string.IsNullOrWhiteSpace(status.P2PPath) ? "备用路径" : $"{status.P2PPath} · {status.P2PRttMs} ms";
            DownloadRateText.Text = FormatRate(traffic?.DownloadRate ?? 0);
            UploadRateText.Text = FormatRate(traffic?.UploadRate ?? 0);
            _tray?.UpdateRuntimeState(
                status.Connected,
                status.ProxyPaused,
                status.Transport,
                ExitNameText.Text,
                DownloadRateText.Text,
                UploadRateText.Text);
            OverviewRecentConnectionsPanel.Children.Clear();
            foreach (var connection in (traffic?.Connections ?? []).OrderByDescending(x => x.Id).Take(5))
            {
                var process = !string.IsNullOrWhiteSpace(connection.ProcessName) ? connection.ProcessName : (!string.IsNullOrWhiteSpace(connection.Process) ? Path.GetFileName(connection.Process) : "未知进程");
                var target = $"{(!string.IsNullOrWhiteSpace(connection.Host) ? connection.Host : connection.Ip)}:{connection.Port}";
                OverviewRecentConnectionsPanel.Children.Add(TwoLine(process, $"{target} · {ConnectionPathLabel(connection)} · {FormatRate(connection.DownloadRate)} ↓"));
            }
            if (OverviewRecentConnectionsPanel.Children.Count == 0)
                OverviewRecentConnectionsPanel.Children.Add(new TextBlock { Text = "暂无连接记录", Foreground = ThemeBrush("TextFillColorSecondaryBrush") });
            await PollMessagesAsync(render: false);
        }
        catch (Exception ex)
        {
            OverviewBar.Message = ex.Message;
            OverviewBar.IsOpen = true;
        }
    }

    private async Task ShowApprovalStateDialogAsync(AgentStatusDto status)
    {
        var title = status.ApprovalState switch
        {
            "pending" => "设备正在等待审批",
            "rejected" => "设备审批已被拒绝",
            "revoked" => "设备授权已被撤销",
            _ => "设备审批状态已变化"
        };
        var detail = status.ApprovalState switch
        {
            "pending" => "请在 RelayProxy Server 管理控制台审批当前设备后再使用代理、RDP 与消息能力。",
            "rejected" => "服务端拒绝了当前设备。请检查身份和申请能力，必要时重新发起审批。",
            "revoked" => "服务端已撤销当前设备授权。现有资源访问将被停止。",
            _ => ""
        };
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = title,
            Content = $"{detail}\n\n设备 ID：{status.DeviceId}",
            PrimaryButtonText = "复制设备 ID",
            CloseButtonText = "关闭",
            DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowAsync() == ContentDialogResult.Primary && !string.IsNullOrWhiteSpace(status.DeviceId))
        {
            var package = new DataPackage();
            package.SetText(status.DeviceId);
            Clipboard.SetContent(package);
        }
    }

    private async Task ShowExitUnavailableDialogAsync(string exitId, string? exitName, bool authorizationMissing)
    {
        var name = string.IsNullOrWhiteSpace(exitName) ? exitId : exitName;
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = authorizationMissing ? "当前出口已不可用" : "当前出口已离线",
            Content = authorizationMissing
                ? $"之前选择的出口“{name}”已不在当前身份授权列表中。RelayProxy 不会把这个状态伪装成正常出口。"
                : $"出口“{name}”当前离线。可以选择其他在线出口，或等待它恢复。",
            PrimaryButtonText = "选择其他出口",
            CloseButtonText = "知道了",
            DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowAsync() == ContentDialogResult.Primary)
            SelectNavigation("exits");
    }

    private async Task LoadConfigAsync()
    {
        if (_dirtyPages.Contains(_page)) return;
        _suppressDirtyTracking = true;
        try
        {
            var cfg = await App.AgentApi.GetConfigAsync();
            if (cfg is null || !string.IsNullOrWhiteSpace(cfg.ConfigError)) throw new InvalidOperationException(cfg?.ConfigError ?? "无法读取配置");
            _config = cfg;

            ServerAddressBox.Text = cfg.ServerAddress;
            QuicPortBox.Value = cfg.QuicPort;
            TcpPortBox.Value = cfg.TcpPort;
            TlsSwitch.IsOn = cfg.TlsEnabled;
            DeviceNameBox.Text = cfg.DeviceName;
            IdentityBox.Text = cfg.IdentityId;
            SelectComboTag(TransportCombo, cfg.Transport);
            P2PEnabledSwitch.IsOn = cfg.P2P.Enabled;
            SelectComboTag(P2PModeCombo, cfg.P2P.Mode);
            P2PFallbackSwitch.IsOn = cfg.P2P.Fallback;
            PublicAdvertiseBox.Text = cfg.Direct.PublicAdvertise;
            PunchTimeoutBox.Value = cfg.P2P.PunchTimeoutMs;
            KeepaliveBox.Value = cfg.P2P.KeepaliveSec > 0 ? cfg.P2P.KeepaliveSec : 10;
            IdleTimeoutBox.Value = cfg.P2P.IdleTimeoutSec;
            MaxSessionsBox.Value = cfg.P2P.MaxExitSessions;

            SocksEnabledSwitch.IsOn = cfg.Socks5.Enabled;
            SocksListenBox.Text = cfg.Socks5.Listen;
            SocksPortBox.Value = cfg.Socks5.Port;
            HttpEnabledSwitch.IsOn = cfg.Http.Enabled;
            HttpListenBox.Text = cfg.Http.Listen;
            HttpPortBox.Value = cfg.Http.Port;
            TransparentProxySwitch.IsOn = string.Equals(cfg.Network.Mode, "divert", StringComparison.OrdinalIgnoreCase);
            ExcludeProcessesBox.Text = string.Join(Environment.NewLine, cfg.Network.ExcludeProcesses);

            ExitEnabledSwitch.IsOn = cfg.ExitEnabled;
            AllowInternetCheck.IsChecked = cfg.AllowInternet;
            AllowPrivateCheck.IsChecked = cfg.AllowPrivateNetwork;
            AllowLoopbackCheck.IsChecked = cfg.AllowLoopback;
            SelectComboTag(ExitUpstreamModeCombo, cfg.ExitUpstream.Mode);
            ExitUpstreamAddressBox.Text = cfg.ExitUpstream.Address;
            ExitUpstreamUserBox.Text = cfg.ExitUpstream.Username;
            ExitUpstreamPasswordBox.Password = cfg.ExitUpstream.Password;
            UpdateExitUpstreamFields();
            SelectComboTag(AccessModeCombo, cfg.AccessMode);
            AccessDomainsBox.Text = string.Join(Environment.NewLine, cfg.AccessDomains);
            AccessCidrsBox.Text = string.Join(Environment.NewLine, cfg.AccessCidrs);

            SelectComboTag(RoutingModeCombo, cfg.Routing.Mode);
            SelectComboTag(DefaultActionCombo, cfg.Routing.DefaultAction);
            _routingRules = cfg.Routing.Rules.Select(CloneRule).ToList();
            RenderRoutingRules();

            _suppressAutostart = true;
            AutostartSwitch.IsOn = cfg.IsAutostart;
            MinimizeToTraySwitch.IsOn = cfg.MinimizeToTray;
            SystemNotificationsSwitch.IsOn = cfg.SystemNotifications;
            SystemNotificationStateText.Text = App.Notifications.IsRegistered
                ? "Windows 系统通知已注册；点击通知会前置 RelayProxy 窗口。"
                : $"Windows 系统通知不可用：{(string.IsNullOrWhiteSpace(App.Notifications.LastError) ? "注册失败" : App.Notifications.LastError)}";
            SelectComboTag(ThemeCombo, cfg.Theme);
            PopupTimeoutBox.Value = cfg.VerificationPopupTimeoutSec;
            _suppressAutostart = false;
            RestartAgentButton.Visibility = cfg.RestartRequired && App.AgentHost.CanRestart ? Visibility.Visible : Visibility.Collapsed;
            ApplyTheme(cfg.Theme);
        }
        catch (Exception ex)
        {
            if (_page == "connection") ShowInfo(ConnectionBar, "读取配置失败", ex.Message, InfoBarSeverity.Error);
            else if (_page == "proxy") ShowInfo(ProxyBar, "读取配置失败", ex.Message, InfoBarSeverity.Error);
            else if (_page == "routing") ShowInfo(RoutingBar, "读取配置失败", ex.Message, InfoBarSeverity.Error);
            else if (_page == "exitshare") ShowInfo(ExitShareBar, "读取配置失败", ex.Message, InfoBarSeverity.Error);
            else if (_page == "settings") ShowInfo(SettingsBar, "读取配置失败", ex.Message, InfoBarSeverity.Error);
        }
        finally
        {
            _suppressDirtyTracking = false;
        }
    }

    private async Task RefreshExitsAsync()
    {
        try
        {
            var exits = await App.AgentApi.GetExitsAsync();
            ExitsPanel.Children.Clear();
            foreach (var exit in SortExits(exits))
            {
                var pathText = exit.Direct?.Public?.Available == true ? $"Public Direct · {exit.Direct.Public.Transport.ToUpperInvariant()}" : "P2P / Relay";
                var testing = _speedTestingExits.Contains(exit.DeviceId);
                _speedTests.TryGetValue(exit.DeviceId, out var lastTest);

                var selectButton = new Button { Content = "设为默认", Tag = exit.DeviceId, VerticalAlignment = VerticalAlignment.Center, IsEnabled = exit.Online && !testing };
                selectButton.Click += async (_, _) =>
                {
                    try { await App.AgentApi.SelectExitAsync(exit.DeviceId); await RefreshExitsAsync(); await RefreshOverviewAsync(); }
                    catch (Exception ex) { ShowInfo(ExitsBar, "切换失败", ex.Message, InfoBarSeverity.Error); }
                };

                var speedButton = new Button { Content = testing ? "测速中…" : "测速", Tag = exit.DeviceId, VerticalAlignment = VerticalAlignment.Center, IsEnabled = exit.Online && !testing };
                speedButton.Click += async (_, _) =>
                {
                    if (!_speedTestingExits.Add(exit.DeviceId)) return;
                    await RefreshExitsAsync();
                    try
                    {
                        var result = await App.AgentApi.RunSpeedTestAsync(exit.DeviceId, SafeInt(SpeedDurationBox, 2));
                        if (result is null) throw new InvalidOperationException("测速没有返回结果。");
                        _speedTests[exit.DeviceId] = result;
                        ShowInfo(
                            ExitsBar,
                            $"{(string.IsNullOrWhiteSpace(exit.Name) ? exit.DeviceId : exit.Name)} 测速完成",
                            $"↓ {result.Download.MegabitsPerSecond:0.0} Mbps · ↑ {result.Upload.MegabitsPerSecond:0.0} Mbps · {result.Download.Path}",
                            InfoBarSeverity.Success);
                    }
                    catch (Exception ex)
                    {
                        ShowInfo(ExitsBar, "测速失败", ex.Message, InfoBarSeverity.Error);
                    }
                    finally
                    {
                        _speedTestingExits.Remove(exit.DeviceId);
                        await RefreshExitsAsync();
                    }
                };

                var grid = new Grid { ColumnSpacing = 14 };
                grid.ColumnDefinitions.Add(new ColumnDefinition()); grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                var stack = new StackPanel { Spacing = 3 };
                stack.Children.Add(new TextBlock { Text = string.IsNullOrWhiteSpace(exit.Name) ? exit.DeviceId : exit.Name, FontSize = 15, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
                stack.Children.Add(new TextBlock { Text = $"{(exit.Online ? "在线" : "离线")} · {pathText} · {exit.IdentityName}", Foreground = ThemeBrush("TextFillColorSecondaryBrush") });
                if (lastTest is not null)
                    stack.Children.Add(new TextBlock { Text = $"上次测速  ↓ {lastTest.Download.MegabitsPerSecond:0.0} Mbps  ↑ {lastTest.Upload.MegabitsPerSecond:0.0} Mbps  ·  {lastTest.Download.Path}", Foreground = ThemeBrush("TextFillColorSecondaryBrush") });

                var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, VerticalAlignment = VerticalAlignment.Center };
                actions.Children.Add(speedButton);
                actions.Children.Add(selectButton);
                grid.Children.Add(stack);
                Grid.SetColumn(actions, 1);
                grid.Children.Add(actions);
                ExitsPanel.Children.Add(Card(grid));
            }
            if (exits.Count == 0) ShowInfo(ExitsBar, "暂无授权出口", "请确认当前身份已审批设备，并已授权可用出口。", InfoBarSeverity.Informational);
            else ExitsBar.IsOpen = false;
        }
        catch (Exception ex) { ShowInfo(ExitsBar, "正在等待服务端出口", ex.Message, InfoBarSeverity.Warning); }
    }

    private async void ExitSort_Changed(object sender, SelectionChangedEventArgs e) => await RefreshExitsAsync();

    private IEnumerable<ProxyExitDto> SortExits(IEnumerable<ProxyExitDto> exits)
    {
        var sort = (ExitSortCombo?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "default";
        return sort switch
        {
            "download" => exits
                .OrderByDescending(x => x.Online)
                .ThenByDescending(x => _speedTests.TryGetValue(x.DeviceId, out var result) ? result.Download.MegabitsPerSecond : -1)
                .ThenBy(x => x.Name),
            "upload" => exits
                .OrderByDescending(x => x.Online)
                .ThenByDescending(x => _speedTests.TryGetValue(x.DeviceId, out var result) ? result.Upload.MegabitsPerSecond : -1)
                .ThenBy(x => x.Name),
            "name" => exits
                .OrderByDescending(x => x.Online)
                .ThenBy(x => string.IsNullOrWhiteSpace(x.Name) ? x.DeviceId : x.Name),
            _ => exits
                .OrderByDescending(x => x.Online)
                .ThenBy(x => string.IsNullOrWhiteSpace(x.Name) ? x.DeviceId : x.Name)
        };
    }

    private async void SpeedAll_Click(object sender, RoutedEventArgs e)
    {
        if (_speedTestingExits.Count > 0)
        {
            ShowInfo(ExitsBar, "测速正在进行", "请等待当前测速完成后再开始批量测速。", InfoBarSeverity.Informational);
            return;
        }

        try
        {
            var exits = (await App.AgentApi.GetExitsAsync()).Where(x => x.Online).ToList();
            if (exits.Count == 0)
            {
                ShowInfo(ExitsBar, "没有可测速出口", "当前没有在线且已授权的出口。", InfoBarSeverity.Informational);
                return;
            }

            var duration = SafeInt(SpeedDurationBox, 2);
            foreach (var exit in exits) _speedTestingExits.Add(exit.DeviceId);
            await RefreshExitsAsync();

            var succeeded = 0;
            var failed = 0;
            foreach (var exit in exits)
            {
                try
                {
                    var result = await App.AgentApi.RunSpeedTestAsync(exit.DeviceId, duration);
                    if (result is null) throw new InvalidOperationException("测速没有返回结果。");
                    _speedTests[exit.DeviceId] = result;
                    succeeded++;
                }
                catch
                {
                    failed++;
                }
                finally
                {
                    _speedTestingExits.Remove(exit.DeviceId);
                    await RefreshExitsAsync();
                }
            }

            ShowInfo(
                ExitsBar,
                "批量测速完成",
                $"成功 {succeeded} 个 · 失败 {failed} 个 · 每方向 {duration} 秒",
                failed == 0 ? InfoBarSeverity.Success : InfoBarSeverity.Warning);
        }
        catch (Exception ex)
        {
            _speedTestingExits.Clear();
            await RefreshExitsAsync();
            ShowInfo(ExitsBar, "批量测速失败", ex.Message, InfoBarSeverity.Error);
        }
    }

    private async Task RefreshConnectionsAsync()
    {
        try
        {
            var snapshot = await App.AgentApi.GetConnectionsAsync();
            if (snapshot is null) return;
            ActiveConnectionsText.Text = snapshot.Active.ToString();
            ConnectionsDownText.Text = FormatRate(snapshot.DownloadRate);
            ConnectionsUpText.Text = FormatRate(snapshot.UploadRate);
            _connectionCache = snapshot.Connections;
            RenderConnections();
        }
        catch { }
    }

    private void ConnectionFilter_Changed(object sender, TextChangedEventArgs e) => RenderConnections();
    private void ConnectionFilter_Changed(object sender, SelectionChangedEventArgs e) => RenderConnections();

    private void RenderConnections()
    {
        if (ConnectionsPanel is null) return;
        var search = ConnectionSearchBox?.Text?.Trim() ?? "";
        var statusFilter = (ConnectionStatusFilter?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "all";
        var protocolFilter = (ConnectionProtocolFilter?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "all";
        var actionFilter = (ConnectionActionFilter?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "all";
        var pathFilter = (ConnectionPathFilter?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "all";
        var sort = (ConnectionSortCombo?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "newest";

        IEnumerable<ConnectionDto> query = _connectionCache;
        if (!string.IsNullOrWhiteSpace(search))
            query = query.Where(c => ConnectionSearchText(c).Contains(search, StringComparison.CurrentCultureIgnoreCase));
        if (statusFilter != "all")
            query = query.Where(c => ConnectionStatusValue(c) == statusFilter);
        if (protocolFilter != "all")
            query = query.Where(c => string.Equals(c.Protocol, protocolFilter, StringComparison.OrdinalIgnoreCase));
        if (actionFilter != "all")
            query = query.Where(c => ConnectionActionValue(c) == actionFilter);
        if (pathFilter != "all")
            query = query.Where(c => ConnectionPathValue(c) == pathFilter);

        query = sort switch
        {
            "download" => query.OrderByDescending(c => c.DownloadRate).ThenByDescending(c => c.Id),
            "upload" => query.OrderByDescending(c => c.UploadRate).ThenByDescending(c => c.Id),
            _ => query.OrderByDescending(c => c.Id)
        };

        var visible = query.Take(120).ToList();
        ConnectionsPanel.Children.Clear();
        foreach (var connection in visible)
        {
            var target = $"{(!string.IsNullOrWhiteSpace(connection.Host) ? connection.Host : connection.Ip)}:{connection.Port}";
            var process = !string.IsNullOrWhiteSpace(connection.ProcessName) ? connection.ProcessName : (!string.IsNullOrWhiteSpace(connection.Process) ? Path.GetFileName(connection.Process) : "未知进程");
            var grid = new Grid { ColumnSpacing = 12 };
            for (var i = 0; i < 5; i++) grid.ColumnDefinitions.Add(new ColumnDefinition { Width = i < 2 ? new GridLength(1, GridUnitType.Star) : GridLength.Auto });
            grid.Children.Add(TwoLine(process, string.IsNullOrWhiteSpace(connection.Rule) ? $"{connection.Action} · {connection.Protocol.ToUpperInvariant()}" : connection.Rule));
            var dest = TwoLine(target, $"{ConnectionPathLabel(connection)} · {connection.ExitId}"); Grid.SetColumn(dest, 1); grid.Children.Add(dest);
            var down = new TextBlock { Text = FormatRate(connection.DownloadRate), VerticalAlignment = VerticalAlignment.Center }; Grid.SetColumn(down, 2); grid.Children.Add(down);
            var up = new TextBlock { Text = FormatRate(connection.UploadRate), VerticalAlignment = VerticalAlignment.Center }; Grid.SetColumn(up, 3); grid.Children.Add(up);
            var state = new TextBlock { Text = connection.State, VerticalAlignment = VerticalAlignment.Center }; Grid.SetColumn(state, 4); grid.Children.Add(state);
            ConnectionsPanel.Children.Add(Card(grid));
        }

        if (visible.Count == 0)
            ConnectionsPanel.Children.Add(Card(TwoLine("没有匹配的连接", "调整搜索、状态、协议、动作、路径或排序条件后重试。")));
    }

    private static string ConnectionSearchText(ConnectionDto c) =>
        string.Join("\n", c.ProcessName, c.Process, c.Host, c.Ip, c.Port.ToString(), c.Protocol, c.Action, c.Rule, c.ExitId, c.Path, c.State);

    private static string ConnectionStatusValue(ConnectionDto c)
    {
        var state = (c.State ?? "").Trim().ToLowerInvariant();
        if (state.Contains("block") || state.Contains("reject") || state.Contains("deny") ||
            string.Equals(c.Action, "REJECT", StringComparison.OrdinalIgnoreCase))
            return "blocked";
        if (state.Contains("connect") || state.Contains("dial") || state.Contains("handshake") || state.Contains("pending"))
            return "connecting";
        if (state.Contains("closed") || state.Contains("ended") || state.Contains("done") ||
            state.Contains("failed") || state.Contains("error") || state.Contains("timeout"))
            return "ended";
        return "active";
    }

    private static string ConnectionActionValue(ConnectionDto c)
    {
        return (c.Action ?? "").Trim().ToUpperInvariant() switch
        {
            "DIRECT" => "direct",
            "REJECT" => "reject",
            _ => "proxy"
        };
    }

    private static string ConnectionPathValue(ConnectionDto c)
    {
        var value = (c.Path ?? "").ToLowerInvariant();
        if (value.Contains("p2p")) return "p2p";
        if (value.Contains("direct")) return "direct";
        if (value.Contains("relay")) return "relay";
        return string.Equals(c.Action, "DIRECT", StringComparison.OrdinalIgnoreCase) ? "direct" : "relay";
    }

    private static string ConnectionPathLabel(ConnectionDto c) => ConnectionPathValue(c) switch
    {
        "direct" => "Direct",
        "p2p" => "P2P",
        _ => "Relay"
    };

    private async Task RefreshMessagesAsync() => await PollMessagesAsync(render: true);

    private void MessageFilter_Changed(object sender, TextChangedEventArgs e) => RenderMessages();
    private void MessageFilter_Changed(object sender, SelectionChangedEventArgs e) => RenderMessages();

    private void RenderMessages()
    {
        if (MessagesPanel is null) return;

        var search = MessageSearchBox?.Text?.Trim() ?? "";
        var filter = (MessageTypeFilter?.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "all";
        var visible = _messageCache
            .Where(m => filter == "all" || string.Equals(MessageTypeValue(m), filter, StringComparison.OrdinalIgnoreCase))
            .Where(m => string.IsNullOrWhiteSpace(search) || MessageSearchText(m).Contains(search, StringComparison.CurrentCultureIgnoreCase))
            .OrderByDescending(m => m.CreatedAt)
            .Take(200)
            .ToList();

        MessagesPanel.Children.Clear();
        foreach (var m in visible)
        {
            var grid = new Grid { ColumnSpacing = 12 };
            grid.ColumnDefinitions.Add(new ColumnDefinition());
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            var type = PopupTypeLabel(m);
            var when = FormatCreatedAt(m.CreatedAt);
            var metadata = string.Join(" · ", new[] { m.Source, string.IsNullOrWhiteSpace(m.MessageRule) ? "" : $"规则：{m.MessageRule}", when }.Where(x => !string.IsNullOrWhiteSpace(x)));
            grid.Children.Add(TwoLine(
                $"{type} · {(string.IsNullOrWhiteSpace(m.Title) ? "RelayProxy 消息" : m.Title)}",
                $"{m.Content}\n{metadata}"));

            var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, VerticalAlignment = VerticalAlignment.Center };
            if (!string.IsNullOrWhiteSpace(m.VerificationCode))
            {
                var copyButton = new Button { Content = $"复制 {m.VerificationCode}", Tag = m.VerificationCode };
                copyButton.Click += (_, _) =>
                {
                    var p = new DataPackage();
                    p.SetText((string)copyButton.Tag);
                    Clipboard.SetContent(p);
                    ShowInfo(MessagesBar, "验证码已复制", (string)copyButton.Tag, InfoBarSeverity.Success);
                };
                actions.Children.Add(copyButton);
            }
            if (MessageTypeValue(m) == "important")
            {
                var diagnosticsButton = new Button { Content = "查看诊断" };
                diagnosticsButton.Click += (_, _) => SelectNavigation("diagnostics");
                actions.Children.Add(diagnosticsButton);
            }
            var detailsButton = new Button { Content = "详情" };
            detailsButton.Click += async (_, _) => await ShowMessageDetailsAsync(m);
            actions.Children.Add(detailsButton);

            Grid.SetColumn(actions, 1);
            grid.Children.Add(actions);
            MessagesPanel.Children.Add(Card(grid));
        }

        if (visible.Count == 0)
            MessagesPanel.Children.Add(Card(TwoLine("没有匹配的消息", "调整搜索关键词或消息类型筛选后重试。")));
    }

    private static string MessageSearchText(PushMessageDto m) =>
        string.Join("\n", m.Title, m.Content, m.Source, m.MessageRule, m.VerificationCode);

    private async void ServerConsoleLogin_Click(object sender, RoutedEventArgs e)
    {
        ServerConsoleLoginButton.IsEnabled = false;
        try
        {
            var user = await _serverConsole.LoginAsync(ServerConsoleUrlBox.Text, ServerConsoleUserBox.Text, ServerConsolePasswordBox.Password);
            ServerConsolePasswordBox.Password = "";
            ServerConsoleStateText.Text = $"已登录：{(string.IsNullOrWhiteSpace(user.DisplayName) ? user.Username : user.DisplayName)} · {user.Role} · Identity {user.IdentityId}";
            ServerConsoleLoginButton.Visibility = Visibility.Collapsed;
            ServerConsoleLogoutButton.Visibility = Visibility.Visible;
            ServerRuleEditorPanel.Visibility = Visibility.Visible;
            await RefreshServerChannelsAsync(preserveSelection: false);
            ShowInfo(MessagesBar, "Server Console 已登录", "只使用本次内存会话管理当前账号可见的 Message Channel。", InfoBarSeverity.Success);
        }
        catch (Exception ex)
        {
            ServerConsoleStateText.Text = "登录失败；Agent 隧道身份不会被用于 Server 管理操作。";
            ShowInfo(MessagesBar, "Server Console 登录失败", ex.Message, InfoBarSeverity.Error);
        }
        finally
        {
            ServerConsoleLoginButton.IsEnabled = true;
        }
    }

    private async void ServerConsoleLogout_Click(object sender, RoutedEventArgs e)
    {
        if (_dirtyPages.Contains("messages") && !await ConfirmDiscardChangesAsync("退出 Server Console"))
            return;

        _dirtyPages.Remove("messages");
        await _serverConsole.LogoutAsync();
        ResetServerConsoleEditor();
        ServerConsoleStateText.Text = "未登录；Agent 隧道身份不会被用于 Server 管理操作。";
    }

    private async void RefreshServerChannels_Click(object sender, RoutedEventArgs e)
    {
        if (_dirtyPages.Contains("messages") && !await ConfirmDiscardChangesAsync("刷新 Message Channel"))
            return;

        _dirtyPages.Remove("messages");
        await RefreshServerChannelsAsync(preserveSelection: true);
    }

    private async Task RefreshServerChannelsAsync(bool preserveSelection)
    {
        if (!_serverConsole.IsAuthenticated) return;
        try
        {
            var selectedId = preserveSelection ? _selectedMessageChannel?.Id : "";
            _serverChannels = (await _serverConsole.GetMessageChannelsAsync()).ToList();

            _suppressMessageChannelSelection = true;
            MessageChannelCombo.Items.Clear();
            foreach (var channel in _serverChannels.OrderBy(x => x.Name))
            {
                MessageChannelCombo.Items.Add(new ComboBoxItem
                {
                    Content = string.IsNullOrWhiteSpace(channel.Name) ? channel.Id : channel.Name,
                    Tag = channel
                });
            }

            var selected = !string.IsNullOrWhiteSpace(selectedId)
                ? MessageChannelCombo.Items.OfType<ComboBoxItem>().FirstOrDefault(x => (x.Tag as MessageChannelDto)?.Id == selectedId)
                : null;
            if (selected is null && MessageChannelCombo.Items.Count > 0)
                selected = MessageChannelCombo.Items[0] as ComboBoxItem;
            MessageChannelCombo.SelectedItem = selected;
            _suppressMessageChannelSelection = false;

            LoadSelectedMessageChannel(selected?.Tag as MessageChannelDto);
        }
        catch (Exception ex)
        {
            _suppressMessageChannelSelection = false;
            ShowInfo(MessagesBar, "读取 Message Channel 失败", ex.Message, InfoBarSeverity.Error);
        }
    }

    private async void MessageChannel_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (_suppressMessageChannelSelection) return;
        var next = (MessageChannelCombo.SelectedItem as ComboBoxItem)?.Tag as MessageChannelDto;
        if (next?.Id == _selectedMessageChannel?.Id) return;

        if (_dirtyPages.Contains("messages"))
        {
            if (!await ConfirmDiscardChangesAsync("切换 Message Channel"))
            {
                _suppressMessageChannelSelection = true;
                var current = MessageChannelCombo.Items.OfType<ComboBoxItem>()
                    .FirstOrDefault(x => (x.Tag as MessageChannelDto)?.Id == _selectedMessageChannel?.Id);
                MessageChannelCombo.SelectedItem = current;
                _suppressMessageChannelSelection = false;
                return;
            }
            _dirtyPages.Remove("messages");
        }

        LoadSelectedMessageChannel(next);
    }

    private void LoadSelectedMessageChannel(MessageChannelDto? channel)
    {
        _selectedMessageChannel = channel;
        _messageRulesDraft = channel?.MessageRules.Select(x => x.Clone()).ToList() ?? [];
        if (channel is null)
        {
            MessageChannelMetaText.Text = "选择一个 Message Channel 后编辑规则。";
        }
        else
        {
            var target = channel.AllDevices
                ? "所有审批设备"
                : channel.DeviceIds.Count == 0 ? "无目标设备" : $"{channel.DeviceIds.Count} 个指定设备";
            MessageChannelMetaText.Text = $"{channel.Name} · {target} · {_messageRulesDraft.Count} 条规则";
        }
        RenderMessageRules();
    }

    private void ResetServerConsoleEditor()
    {
        _serverChannels.Clear();
        _selectedMessageChannel = null;
        _messageRulesDraft.Clear();
        _suppressMessageChannelSelection = true;
        MessageChannelCombo.Items.Clear();
        MessageChannelCombo.SelectedItem = null;
        _suppressMessageChannelSelection = false;
        MessageRulesPanel.Children.Clear();
        MessageChannelMetaText.Text = "选择一个 Message Channel 后编辑规则。";
        ServerRuleEditorPanel.Visibility = Visibility.Collapsed;
        ServerConsoleLoginButton.Visibility = Visibility.Visible;
        ServerConsoleLogoutButton.Visibility = Visibility.Collapsed;
    }

    private void RestoreMessageRuleDraft()
    {
        if (_selectedMessageChannel is null) return;
        _messageRulesDraft = _selectedMessageChannel.MessageRules.Select(x => x.Clone()).ToList();
        RenderMessageRules();
    }

    private async void AddMessageRule_Click(object sender, RoutedEventArgs e)
    {
        if (_selectedMessageChannel is null)
        {
            ShowInfo(MessagesBar, "尚未选择 Message Channel", "", InfoBarSeverity.Warning);
            return;
        }

        var rule = new MessageRuleDto
        {
            Name = "新消息规则",
            Type = "message",
            Enabled = true,
            Match = new MessageMatchDto { MatchType = "all", KeywordMode = "any" },
            Popup = true,
            PopupType = "message"
        };
        if (await EditMessageRuleAsync(rule, isNew: true))
        {
            _messageRulesDraft.Add(rule);
            MarkDirty("messages");
            RenderMessageRules();
        }
    }

    private void RenderMessageRules()
    {
        if (MessageRulesPanel is null) return;
        MessageRulesPanel.Children.Clear();

        for (var index = 0; index < _messageRulesDraft.Count; index++)
        {
            var rule = _messageRulesDraft[index];
            var row = new Grid { ColumnSpacing = 12 };
            row.ColumnDefinitions.Add(new ColumnDefinition());
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });

            var summary = RuleSummary(rule);
            row.Children.Add(TwoLine(
                $"{(rule.Enabled ? "✓" : "—")} {rule.Name}{(rule.Default ? " · 默认" : "")}",
                summary));

            var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
            var toggle = new Button { Content = rule.Enabled ? "停用" : "启用" };
            toggle.Click += (_, _) =>
            {
                rule.Enabled = !rule.Enabled;
                MarkDirty("messages");
                RenderMessageRules();
            };
            actions.Children.Add(toggle);

            var edit = new Button { Content = "编辑" };
            edit.Click += async (_, _) =>
            {
                if (await EditMessageRuleAsync(rule, isNew: false))
                {
                    MarkDirty("messages");
                    RenderMessageRules();
                }
            };
            actions.Children.Add(edit);

            if (!rule.Default)
            {
                if (index > 0 && !_messageRulesDraft[index - 1].Default)
                {
                    var up = new Button { Content = "↑" };
                    up.Click += (_, _) =>
                    {
                        var current = _messageRulesDraft.IndexOf(rule);
                        if (current <= 0) return;
                        (_messageRulesDraft[current - 1], _messageRulesDraft[current]) = (_messageRulesDraft[current], _messageRulesDraft[current - 1]);
                        MarkDirty("messages");
                        RenderMessageRules();
                    };
                    actions.Children.Add(up);
                }

                var defaultIndex = _messageRulesDraft.FindIndex(x => x.Default);
                var currentIndex = _messageRulesDraft.IndexOf(rule);
                var maxCustomIndex = defaultIndex >= 0 ? defaultIndex - 1 : _messageRulesDraft.Count - 1;
                if (currentIndex >= 0 && currentIndex < maxCustomIndex)
                {
                    var down = new Button { Content = "↓" };
                    down.Click += (_, _) =>
                    {
                        var current = _messageRulesDraft.IndexOf(rule);
                        if (current < 0 || current + 1 >= _messageRulesDraft.Count || _messageRulesDraft[current + 1].Default) return;
                        (_messageRulesDraft[current + 1], _messageRulesDraft[current]) = (_messageRulesDraft[current], _messageRulesDraft[current + 1]);
                        MarkDirty("messages");
                        RenderMessageRules();
                    };
                    actions.Children.Add(down);
                }

                var delete = new Button { Content = "删除" };
                delete.Click += async (_, _) =>
                {
                    var dialog = new ContentDialog
                    {
                        XamlRoot = Content.XamlRoot,
                        Title = "删除消息规则",
                        Content = $"确定删除“{rule.Name}”吗？",
                        PrimaryButtonText = "删除",
                        CloseButtonText = "取消",
                        DefaultButton = ContentDialogButton.Close
                    };
                    if (await dialog.ShowAsync() != ContentDialogResult.Primary) return;
                    _messageRulesDraft.Remove(rule);
                    MarkDirty("messages");
                    RenderMessageRules();
                };
                actions.Children.Add(delete);
            }

            Grid.SetColumn(actions, 1);
            row.Children.Add(actions);
            MessageRulesPanel.Children.Add(Card(row));
        }

        if (_messageRulesDraft.Count == 0)
            MessageRulesPanel.Children.Add(Card(TwoLine("暂无消息规则", "新增规则后保存到当前 Message Channel。")));
    }

    private async Task<bool> EditMessageRuleAsync(MessageRuleDto rule, bool isNew)
    {
        var draft = rule.Clone();
        if (string.IsNullOrWhiteSpace(draft.PopupType))
            draft.PopupType = draft.Type;

        var name = new TextBox { Header = "规则名称", Text = draft.Name };
        var type = new ComboBox { Header = "消息分类", HorizontalAlignment = HorizontalAlignment.Stretch };
        type.Items.Add(new ComboBoxItem { Content = "验证码", Tag = "verification_code" });
        type.Items.Add(new ComboBoxItem { Content = "普通消息", Tag = "message" });
        type.Items.Add(new ComboBoxItem { Content = "重要提醒", Tag = "important" });
        SelectComboTag(type, draft.Type);

        var enabled = new ToggleSwitch { Header = "启用规则", IsOn = draft.Enabled };
        var popup = new ToggleSwitch { Header = "命中后弹窗", IsOn = draft.Popup ?? true };
        var popupType = new ComboBox { Header = "弹窗类型", HorizontalAlignment = HorizontalAlignment.Stretch };
        popupType.Items.Add(new ComboBoxItem { Content = "验证码弹窗", Tag = "verification_code" });
        popupType.Items.Add(new ComboBoxItem { Content = "普通消息弹窗", Tag = "message" });
        popupType.Items.Add(new ComboBoxItem { Content = "重要提醒弹窗", Tag = "important" });
        SelectComboTag(popupType, draft.PopupType);

        var matchType = new ComboBox { Header = "匹配方式", HorizontalAlignment = HorizontalAlignment.Stretch };
        matchType.Items.Add(new ComboBoxItem { Content = "关键词", Tag = "keywords" });
        matchType.Items.Add(new ComboBoxItem { Content = "包含文本", Tag = "contains" });
        matchType.Items.Add(new ComboBoxItem { Content = "正则表达式", Tag = "regex" });
        matchType.Items.Add(new ComboBoxItem { Content = "全部消息", Tag = "all" });
        SelectComboTag(matchType, string.IsNullOrWhiteSpace(draft.Match.MatchType) ? "all" : draft.Match.MatchType);

        var keywords = new TextBox
        {
            Header = "关键词（逗号或换行分隔）",
            Text = string.Join(Environment.NewLine, draft.Match.Keywords),
            AcceptsReturn = true,
            TextWrapping = TextWrapping.Wrap,
            MinHeight = 72
        };
        var keywordMode = new ComboBox { Header = "关键词模式", HorizontalAlignment = HorizontalAlignment.Stretch };
        keywordMode.Items.Add(new ComboBoxItem { Content = "任一命中", Tag = "any" });
        keywordMode.Items.Add(new ComboBoxItem { Content = "全部命中", Tag = "all" });
        SelectComboTag(keywordMode, string.IsNullOrWhiteSpace(draft.Match.KeywordMode) ? "any" : draft.Match.KeywordMode);

        var matchPattern = new TextBox
        {
            Header = "匹配文本 / 正则",
            Text = draft.Match.Pattern,
            AcceptsReturn = true,
            TextWrapping = TextWrapping.Wrap
        };
        var caseSensitive = new ToggleSwitch { Header = "区分大小写", IsOn = draft.Match.CaseSensitive };

        var extractorType = new ComboBox { Header = "验证码提取方式", HorizontalAlignment = HorizontalAlignment.Stretch };
        extractorType.Items.Add(new ComboBoxItem { Content = "智能提取", Tag = "auto" });
        extractorType.Items.Add(new ComboBoxItem { Content = "正则提取", Tag = "regex" });
        SelectComboTag(extractorType, draft.Verification?.Type ?? "auto");
        var extractorPattern = new TextBox { Header = "验证码提取正则", Text = draft.Verification?.Pattern ?? "" };
        var minLength = new NumberBox { Header = "最短长度", Minimum = 1, Maximum = 64, Value = draft.Verification?.MinLength is > 0 ? draft.Verification.MinLength : 4 };
        var maxLength = new NumberBox { Header = "最长长度", Minimum = 1, Maximum = 64, Value = draft.Verification?.MaxLength is > 0 ? draft.Verification.MaxLength : 8 };
        var maxDistance = new NumberBox { Header = "关键词最大距离", Minimum = 0, Maximum = 1024, Value = draft.Verification?.MaxDistance is > 0 ? draft.Verification.MaxDistance : 64 };
        var allowLetters = new CheckBox { Content = "允许字母", IsChecked = draft.Verification?.AllowLetters ?? true };
        var allowDigits = new CheckBox { Content = "允许数字", IsChecked = draft.Verification?.AllowDigits ?? true };
        var requireDigit = new CheckBox { Content = "至少包含一个数字", IsChecked = draft.Verification?.RequireDigit ?? true };

        var body = new StackPanel { Spacing = 10, MaxWidth = 620 };
        body.Children.Add(name);
        body.Children.Add(type);
        body.Children.Add(enabled);
        body.Children.Add(popup);
        body.Children.Add(popupType);
        body.Children.Add(matchType);
        body.Children.Add(keywords);
        body.Children.Add(keywordMode);
        body.Children.Add(matchPattern);
        body.Children.Add(caseSensitive);
        body.Children.Add(new TextBlock
        {
            Text = draft.Default
                ? "默认验证码规则可以编辑名称、匹配条件、提取方式和弹窗，但不能删除。"
                : "验证码分类需要配置验证码提取；普通消息和重要提醒不会提取验证码。",
            Foreground = ThemeBrush("TextFillColorSecondaryBrush"),
            TextWrapping = TextWrapping.Wrap
        });
        body.Children.Add(extractorType);
        body.Children.Add(extractorPattern);
        var lengths = new Grid { ColumnSpacing = 8 };
        lengths.ColumnDefinitions.Add(new ColumnDefinition());
        lengths.ColumnDefinitions.Add(new ColumnDefinition());
        lengths.ColumnDefinitions.Add(new ColumnDefinition());
        lengths.Children.Add(minLength);
        Grid.SetColumn(maxLength, 1);
        lengths.Children.Add(maxLength);
        Grid.SetColumn(maxDistance, 2);
        lengths.Children.Add(maxDistance);
        body.Children.Add(lengths);
        var extractorFlags = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 12 };
        extractorFlags.Children.Add(allowLetters);
        extractorFlags.Children.Add(allowDigits);
        extractorFlags.Children.Add(requireDigit);
        body.Children.Add(extractorFlags);

        void UpdateEditorVisibility()
        {
            var match = ComboTag(matchType, "all");
            keywords.Visibility = match == "keywords" ? Visibility.Visible : Visibility.Collapsed;
            keywordMode.Visibility = match == "keywords" ? Visibility.Visible : Visibility.Collapsed;
            matchPattern.Visibility = match is "contains" or "regex" ? Visibility.Visible : Visibility.Collapsed;

            var verification = ComboTag(type, "message") == "verification_code";
            extractorType.Visibility = verification ? Visibility.Visible : Visibility.Collapsed;
            var extractor = ComboTag(extractorType, "auto");
            extractorPattern.Visibility = verification && extractor == "regex" ? Visibility.Visible : Visibility.Collapsed;
            lengths.Visibility = verification && extractor == "auto" ? Visibility.Visible : Visibility.Collapsed;
            extractorFlags.Visibility = verification && extractor == "auto" ? Visibility.Visible : Visibility.Collapsed;
            popupType.IsEnabled = popup.IsOn;
        }
        matchType.SelectionChanged += (_, _) => UpdateEditorVisibility();
        type.SelectionChanged += (_, _) => UpdateEditorVisibility();
        extractorType.SelectionChanged += (_, _) => UpdateEditorVisibility();
        popup.Toggled += (_, _) => UpdateEditorVisibility();
        UpdateEditorVisibility();

        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = isNew ? "新增消息规则" : $"编辑规则 · {rule.Name}",
            Content = new ScrollViewer { Content = body, MaxHeight = 620, VerticalScrollBarVisibility = ScrollBarVisibility.Auto },
            PrimaryButtonText = isNew ? "添加" : "保存修改",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Primary
        };

        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return false;

        var ruleName = name.Text.Trim();
        if (string.IsNullOrWhiteSpace(ruleName))
        {
            ShowInfo(MessagesBar, "规则名称不能为空", "", InfoBarSeverity.Warning);
            return false;
        }

        var selectedType = ComboTag(type, "message");
        var selectedMatch = ComboTag(matchType, "all");
        var keywordValues = SplitList(keywords.Text);
        if (selectedMatch == "keywords" && keywordValues.Count == 0)
        {
            ShowInfo(MessagesBar, "关键词规则至少需要一个关键词", "", InfoBarSeverity.Warning);
            return false;
        }
        if (selectedMatch is "contains" or "regex" && string.IsNullOrWhiteSpace(matchPattern.Text))
        {
            ShowInfo(MessagesBar, "匹配文本不能为空", "", InfoBarSeverity.Warning);
            return false;
        }

        VerificationExtractorDto? verification = null;
        if (selectedType == "verification_code")
        {
            var selectedExtractor = ComboTag(extractorType, "auto");
            if (selectedExtractor == "regex" && string.IsNullOrWhiteSpace(extractorPattern.Text))
            {
                ShowInfo(MessagesBar, "验证码提取正则不能为空", "", InfoBarSeverity.Warning);
                return false;
            }
            verification = new VerificationExtractorDto
            {
                Type = selectedExtractor,
                Pattern = extractorPattern.Text.Trim(),
                MinLength = Math.Max(1, SafeInt(minLength, 4)),
                MaxLength = Math.Max(1, SafeInt(maxLength, 8)),
                MaxDistance = Math.Max(0, SafeInt(maxDistance, 64)),
                AllowLetters = allowLetters.IsChecked == true,
                AllowDigits = allowDigits.IsChecked == true,
                RequireDigit = requireDigit.IsChecked == true
            };
            if (verification.MinLength > verification.MaxLength)
            {
                ShowInfo(MessagesBar, "验证码长度范围无效", "最短长度不能大于最长长度。", InfoBarSeverity.Warning);
                return false;
            }
            if (verification.Type == "auto" && !verification.AllowLetters && !verification.AllowDigits)
            {
                ShowInfo(MessagesBar, "验证码提取范围无效", "至少允许字母或数字中的一种。", InfoBarSeverity.Warning);
                return false;
            }
        }

        rule.Name = ruleName;
        rule.Type = selectedType;
        rule.Enabled = enabled.IsOn;
        rule.Popup = popup.IsOn;
        rule.PopupType = ComboTag(popupType, selectedType);
        rule.Match = new MessageMatchDto
        {
            MatchType = selectedMatch,
            Keywords = selectedMatch == "keywords" ? keywordValues : [],
            KeywordMode = ComboTag(keywordMode, "any"),
            Pattern = selectedMatch is "contains" or "regex" ? matchPattern.Text.Trim() : "",
            CaseSensitive = caseSensitive.IsOn
        };
        rule.Verification = verification;
        return true;
    }

    private async void SaveMessageRules_Click(object sender, RoutedEventArgs e)
    {
        if (_selectedMessageChannel is null)
        {
            ShowInfo(MessagesBar, "尚未选择 Message Channel", "", InfoBarSeverity.Warning);
            return;
        }

        try
        {
            var saved = await _serverConsole.UpdateMessageRulesAsync(_selectedMessageChannel, _messageRulesDraft);
            var index = _serverChannels.FindIndex(x => x.Id == saved.Id);
            if (index >= 0) _serverChannels[index] = saved;
            _selectedMessageChannel = saved;
            _messageRulesDraft = saved.MessageRules.Select(x => x.Clone()).ToList();
            _dirtyPages.Remove("messages");
            MessageChannelMetaText.Text = $"{saved.Name} · {(_messageRulesDraft.Count)} 条规则 · 已保存";
            RenderMessageRules();
            ShowInfo(MessagesBar, "消息规则已保存", "Server 已重新校验并返回标准化规则。", InfoBarSeverity.Success);
        }
        catch (Exception ex)
        {
            ShowInfo(MessagesBar, "保存消息规则失败", ex.Message, InfoBarSeverity.Error);
        }
    }

    private static string RuleSummary(MessageRuleDto rule)
    {
        var type = rule.Type switch
        {
            "verification_code" => "验证码",
            "important" => "重要提醒",
            _ => "普通消息"
        };
        var match = rule.Match.MatchType switch
        {
            "keywords" => $"关键词 {string.Join(" / ", rule.Match.Keywords.Take(3))}{(rule.Match.Keywords.Count > 3 ? "…" : "")}",
            "contains" => $"包含“{rule.Match.Pattern}”",
            "regex" => $"正则 {rule.Match.Pattern}",
            _ => "匹配全部"
        };
        var popup = rule.Popup ?? true ? $"弹窗：{PopupTypeName(string.IsNullOrWhiteSpace(rule.PopupType) ? rule.Type : rule.PopupType)}" : "不弹窗";
        return $"{type} · {match} · {popup}";
    }

    private static string PopupTypeName(string value) => value switch
    {
        "verification_code" => "验证码",
        "important" => "重要提醒",
        _ => "普通消息"
    };

    private void TestVerificationPopup_Click(object sender, RoutedEventArgs e) => ShowLocalTestMessage("verification_code");
    private void TestMessagePopup_Click(object sender, RoutedEventArgs e) => ShowLocalTestMessage("message");
    private void TestImportantPopup_Click(object sender, RoutedEventArgs e) => ShowLocalTestMessage("important");

    private void TestPopupQueue_Click(object sender, RoutedEventArgs e)
    {
        ShowLocalTestMessage("verification_code");
        ShowLocalTestMessage("message");
        ShowLocalTestMessage("important");
    }

    private void ShowLocalTestMessage(string type)
    {
        var message = type switch
        {
            "verification_code" => new PushMessageDto
            {
                Id = $"local-test-verification-{Guid.NewGuid():N}",
                Title = "登录验证码",
                Content = "这是 Windows 原生 GUI 的本机验证码弹窗测试。",
                MessageType = "verification_code",
                MessageRule = "本机测试 · 验证码",
                VerificationCode = "731204",
                Popup = true,
                PopupType = "verification_code",
                Source = "Windows GUI 测试",
                CreatedAt = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()
            },
            "important" => new PushMessageDto
            {
                Id = $"local-test-important-{Guid.NewGuid():N}",
                Title = "出口健康状态异常",
                Content = "这是 Windows 原生 GUI 的本机重要提醒测试。可从消息中心直接进入诊断页面。",
                MessageType = "important",
                MessageRule = "本机测试 · 重要提醒",
                Popup = true,
                PopupType = "important",
                Source = "Windows GUI 测试",
                CreatedAt = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()
            },
            _ => new PushMessageDto
            {
                Id = $"local-test-message-{Guid.NewGuid():N}",
                Title = "RelayProxy 普通消息",
                Content = "这是 Windows 原生 GUI 的本机普通消息弹窗测试。",
                MessageType = "message",
                MessageRule = "本机测试 · 普通消息",
                Popup = true,
                PopupType = "message",
                Source = "Windows GUI 测试",
                CreatedAt = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds()
            }
        };

        if (_config?.SystemNotifications == true)
            App.Notifications.TryShow(message);
        _popupWindow ??= new MessagePopupWindow();
        _popupWindow.EnqueueMessage(message, _config?.VerificationPopupTimeoutSec ?? 15);
    }

    private async Task ShowMessageDetailsAsync(PushMessageDto message)
    {
        var type = PopupTypeLabel(message);
        var title = string.IsNullOrWhiteSpace(message.Title) ? "RelayProxy 消息" : message.Title;
        var details = new StackPanel { Spacing = 8, MaxWidth = 560 };
        details.Children.Add(new TextBlock { Text = message.Content, TextWrapping = TextWrapping.Wrap });
        details.Children.Add(new TextBlock
        {
            Text = string.Join(Environment.NewLine, new[]
            {
                $"类型：{type}",
                string.IsNullOrWhiteSpace(message.Source) ? "" : $"来源：{message.Source}",
                string.IsNullOrWhiteSpace(message.MessageRule) ? "" : $"命中规则：{message.MessageRule}",
                string.IsNullOrWhiteSpace(message.VerificationCode) ? "" : $"验证码：{message.VerificationCode}",
                $"时间：{FormatCreatedAt(message.CreatedAt)}"
            }.Where(x => !string.IsNullOrWhiteSpace(x))),
            Foreground = ThemeBrush("TextFillColorSecondaryBrush"),
            TextWrapping = TextWrapping.Wrap
        });

        var important = MessageTypeValue(message) == "important";
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = title,
            Content = details,
            PrimaryButtonText = important ? "查看诊断" : (!string.IsNullOrWhiteSpace(message.VerificationCode) ? "复制验证码" : ""),
            CloseButtonText = "关闭",
            DefaultButton = ContentDialogButton.Close
        };

        var result = await dialog.ShowAsync();
        if (result != ContentDialogResult.Primary) return;
        if (important)
        {
            SelectNavigation("diagnostics");
            return;
        }
        if (!string.IsNullOrWhiteSpace(message.VerificationCode))
        {
            var package = new DataPackage();
            package.SetText(message.VerificationCode);
            Clipboard.SetContent(package);
            ShowInfo(MessagesBar, "验证码已复制", message.VerificationCode, InfoBarSeverity.Success);
        }
    }

    private async Task PollMessagesAsync(bool render)
    {
        try
        {
            var messages = await App.AgentApi.GetMessagesAsync();
            if (!_messageBaselineReady)
            {
                foreach (var m in messages) _seenMessageIds.Add(m.Id);
                _messageBaselineReady = true;
            }
            else
            {
                var fresh = messages.Where(m => !string.IsNullOrWhiteSpace(m.Id) && !_seenMessageIds.Contains(m.Id)).OrderBy(m => m.CreatedAt).ToList();
                foreach (var m in fresh)
                {
                    _seenMessageIds.Add(m.Id);
                    if (MessagePopupWindow.ShouldPopup(m))
                    {
                        if (_config?.SystemNotifications == true)
                            App.Notifications.TryShow(m);
                        _popupWindow ??= new MessagePopupWindow();
                        _popupWindow.EnqueueMessage(m, _config?.VerificationPopupTimeoutSec ?? 15);
                    }
                }
            }

            _messageCache = messages;
            if (!render) return;
            MessagesBar.IsOpen = false;
            RenderMessages();
        }
        catch (Exception ex)
        {
            if (render) ShowInfo(MessagesBar, "消息接口尚未就绪", ex.Message, InfoBarSeverity.Warning);
        }
    }

    private async Task RefreshDevicesAsync()
    {
        try
        {
            var statusTask = App.AgentApi.GetStatusAsync();
            var exitsTask = App.AgentApi.GetExitsAsync();
            var rdpTask = App.AgentApi.GetRdpTargetsAsync();
            await Task.WhenAll(statusTask, exitsTask, rdpTask);
            var status = await statusTask;
            var exits = await exitsTask;
            var targets = await rdpTask;
            if (status is null) return;

            DeviceNameText.Text = string.IsNullOrWhiteSpace(status.DeviceName) ? "当前设备" : status.DeviceName;
            IdentityNameText.Text = string.IsNullOrWhiteSpace(status.IdentityName) ? "Identity 未命名" : status.IdentityName;
            DeviceIdText.Text = string.IsNullOrWhiteSpace(status.DeviceId) ? "—" : status.DeviceId;
            DeviceTransportText.Text = status.Connected ? status.Transport.ToUpperInvariant() : "未连接";
            DeviceApprovalText.Text = status.ApprovalState switch { "approved" => "已审批", "pending" => "待审批", "rejected" => "已拒绝", "revoked" => "已撤销", _ => status.ApprovalState };
            AuthorizedExitCountText.Text = exits.Count.ToString();
            AuthorizedRdpCountText.Text = targets.Count.ToString();

            DeviceCapabilitiesPanel.Children.Clear();
            var approved = new HashSet<string>(status.ApprovedCapabilities, StringComparer.OrdinalIgnoreCase);
            foreach (var capability in new[]
            {
                ("proxy.client", "代理客户端", "可使用授权出口访问网络"),
                ("proxy.exit", "网络出口", "可作为其他授权设备的 Exit"),
                ("rdp.controller", "远程桌面控制端", "可发起已授权 RDP 连接"),
                ("rdp.host", "远程桌面被控端", "可作为 RDP 目标设备"),
                ("rdp.public", "RDP 公网直连", "允许协商 RDP Public Direct 路径")
            })
            {
                var enabled = approved.Contains(capability.Item1);
                DeviceCapabilitiesPanel.Children.Add(TwoLine(
                    $"{(enabled ? "✓" : "—")} {capability.Item2}",
                    enabled ? $"{capability.Item1} · {capability.Item3}" : $"{capability.Item1} · 未批准"));
            }
            DeviceCapabilitiesPanel.Children.Add(TwoLine(
                $"{(status.ApprovalState == "approved" ? "✓" : "—")} 消息接收",
                status.ApprovalState == "approved"
                    ? "审批设备默认可接收服务端推送；消息类型与弹窗由 Message Channel 规则决定"
                    : "设备尚未处于 approved 状态"));

            AuthorizedResourcesPanel.Children.Clear();
            foreach (var exit in exits.OrderByDescending(x => x.Online).ThenBy(x => x.Name))
                AuthorizedResourcesPanel.Children.Add(Card(TwoLine($"出口 · {(string.IsNullOrWhiteSpace(exit.Name) ? exit.DeviceId : exit.Name)}", $"{(exit.Online ? "在线" : "离线")} · {exit.AuthorizationSource} · {exit.DeviceId}")));
            foreach (var target in targets.OrderByDescending(x => x.Online).ThenBy(x => x.Name))
                AuthorizedResourcesPanel.Children.Add(Card(TwoLine($"RDP · {(string.IsNullOrWhiteSpace(target.Name) ? target.DeviceId : target.Name)}", $"{(target.Online ? "在线" : "离线")} · 服务端显式授权 · {target.DeviceId}")));
            DevicesBar.IsOpen = false;
        }
        catch (Exception ex) { ShowInfo(DevicesBar, "设备状态不可用", ex.Message, InfoBarSeverity.Warning); }
    }

    private async Task RefreshRdpAsync()
    {
        try
        {
            var status = await App.AgentApi.GetStatusAsync();
            var targets = await App.AgentApi.GetRdpTargetsAsync();
            if (status is not null)
            {
                RdpListenText.Text = string.IsNullOrWhiteSpace(status.RDPListenAddr) ? "—" : status.RDPListenAddr;
                RdpTcpPathText.Text = string.IsNullOrWhiteSpace(status.RDPPathTCP) ? "—" : status.RDPPathTCP;
                RdpUdpText.Text = status.RDPUDPEnabled ? (status.RDPUDPActive ? $"已激活 · {status.RDPPathUDP}" : "已监听 · 等待 mstsc UDP") : "不可用";
            }
            RdpTargetsPanel.Children.Clear();
            foreach (var target in targets.OrderByDescending(x => x.Online).ThenBy(x => x.Name))
            {
                var grid = new Grid { ColumnSpacing = 12 };
                grid.ColumnDefinitions.Add(new ColumnDefinition()); grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                grid.Children.Add(TwoLine(string.IsNullOrWhiteSpace(target.Name) ? target.DeviceId : target.Name, $"{(target.Online ? "在线" : "离线")} · {target.DeviceId}"));
                var button = new Button { Content = "一键连接", IsEnabled = target.Online, Tag = target.DeviceId, VerticalAlignment = VerticalAlignment.Center };
                button.Click += async (_, _) => await ConnectRdpWithProgressAsync(target);
                Grid.SetColumn(button, 1); grid.Children.Add(button);
                RdpTargetsPanel.Children.Add(Card(grid));
            }
            if (targets.Count == 0) ShowInfo(RdpBar, "暂无授权设备", "服务端未向当前 Agent 授权 RDP 目标。", InfoBarSeverity.Informational);
        }
        catch (Exception ex) { ShowInfo(RdpBar, "RDP 状态不可用", ex.Message, InfoBarSeverity.Warning); }
    }

    private async Task ConnectRdpWithProgressAsync(RdpTargetDto target)
    {
        if (_rdpConnectCts is not null)
        {
            ShowInfo(RdpBar, "已有 RDP 连接正在建立", "请先完成或取消当前连接。", InfoBarSeverity.Informational);
            return;
        }

        var targetName = string.IsNullOrWhiteSpace(target.Name) ? target.DeviceId : target.Name;
        var cts = new CancellationTokenSource();
        _rdpConnectCts = cts;
        var progressText = new TextBlock
        {
            Text = "正在向 Agent 请求 RDP 会话并建立本地回环入口…",
            TextWrapping = TextWrapping.Wrap
        };
        var progress = new ProgressRing
        {
            IsActive = true,
            Width = 30,
            Height = 30,
            HorizontalAlignment = HorizontalAlignment.Left
        };
        var content = new StackPanel { Spacing = 12 };
        content.Children.Add(progress);
        content.Children.Add(progressText);
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = $"正在连接 {targetName}",
            Content = content,
            CloseButtonText = "取消连接",
            DefaultButton = ContentDialogButton.None
        };

        var completed = false;
        dialog.Closing += (_, _) =>
        {
            if (!completed)
            {
                try { cts.Cancel(); } catch { }
            }
        };

        try
        {
            _ = dialog.ShowAsync();
            var connectTask = App.AgentApi.ConnectRdpAsync(target.DeviceId, autoLaunch: true, cts.Token);
            while (!connectTask.IsCompleted)
            {
                await Task.WhenAny(connectTask, Task.Delay(700, cts.Token));
                if (connectTask.IsCompleted) break;
                try
                {
                    var status = await App.AgentApi.GetStatusAsync(cts.Token);
                    if (status is not null && !string.IsNullOrWhiteSpace(status.RDPListenAddr))
                    {
                        var tcp = string.IsNullOrWhiteSpace(status.RDPPathTCP) ? "正在选择" : status.RDPPathTCP;
                        var udp = status.RDPUDPEnabled
                            ? (status.RDPUDPActive ? $"已激活 · {status.RDPPathUDP}" : "已监听，等待 mstsc")
                            : "不可用";
                        progressText.Text = $"本地入口 {status.RDPListenAddr}\nTCP：{tcp}\nUDP：{udp}";
                    }
                }
                catch (OperationCanceledException) { throw; }
                catch { }
            }

            await connectTask;
            completed = true;
            try { dialog.Hide(); } catch { }

            await RefreshRdpAsync();
            ShowInfo(
                RdpBar,
                "RDP 已建立",
                $"已为“{targetName}”建立本地入口并启动 mstsc.exe。TCP：{RdpTcpPathText.Text} · UDP：{RdpUdpText.Text}",
                InfoBarSeverity.Success);
        }
        catch (OperationCanceledException)
        {
            completed = true;
            try { dialog.Hide(); } catch { }
            ShowInfo(RdpBar, "RDP 连接已取消", targetName, InfoBarSeverity.Informational);
        }
        catch (Exception ex)
        {
            completed = true;
            try { dialog.Hide(); } catch { }
            ShowInfo(RdpBar, "RDP 连接失败", ex.Message, InfoBarSeverity.Error);
        }
        finally
        {
            if (ReferenceEquals(_rdpConnectCts, cts))
                _rdpConnectCts = null;
            cts.Dispose();
        }
    }

    private async Task RefreshNetworkServiceAsync()
    {
        if (!App.AgentApi.IsPrivateNativeChannel)
        {
            RepairNetworkServiceButton.IsEnabled = false;
            UninstallNetworkServiceButton.IsEnabled = false;
            NetworkServiceStateText.Text = "仅本机原生会话可管理";
            NetworkServiceDetailText.Text = "当前 GUI 连接的是外部 Agent。Network Service 安装、修复和卸载只开放给本机 WinUI 创建的私有 loopback 管理通道。";
            await PollMessagesAsync(render: false);
            return;
        }

        try
        {
            var status = await App.AgentApi.GetNetworkServiceStatusAsync();
            if (status is null) return;

            RepairNetworkServiceButton.IsEnabled = status.Supported;
            UninstallNetworkServiceButton.IsEnabled = status.Supported && status.Installed;

            if (!status.Supported)
            {
                NetworkServiceStateText.Text = "当前平台 / 架构不支持";
                NetworkServiceDetailText.Text = string.IsNullOrWhiteSpace(status.Message)
                    ? "Windows ARM64 当前不启用 WinDivert 透明代理；SOCKS5 / HTTP 仍可正常使用。"
                    : status.Message;
            }
            else if (status.Ready)
            {
                NetworkServiceStateText.Text = "已安装并运行";
                var details = new List<string> { status.VersionMatch ? "版本匹配" : "版本需更新" };
                if (status.AutoStartKnown) details.Add(status.AutoStart ? "自动启动" : "未设为自动启动");
                if (status.RecoveryKnown) details.Add(status.RecoveryEnabled ? "故障恢复已启用" : "故障恢复未启用");
                if (status.Pid != 0) details.Add($"PID {status.Pid}");
                NetworkServiceDetailText.Text = string.Join(" · ", details);
            }
            else if (status.Installed)
            {
                NetworkServiceStateText.Text = status.VersionMatch ? "已安装，但未就绪" : "版本不匹配，需要修复";
                NetworkServiceDetailText.Text = string.IsNullOrWhiteSpace(status.Message) ? status.State : status.Message;
            }
            else
            {
                NetworkServiceStateText.Text = "尚未安装";
                NetworkServiceDetailText.Text = "启用系统透明代理时需要安装 RelayProxy Network Service；安装/修复会触发 Windows UAC。";
            }
        }
        catch (Exception ex)
        {
            NetworkServiceStateText.Text = "状态读取失败";
            NetworkServiceDetailText.Text = ex.Message;
        }
        await PollMessagesAsync(render: false);
    }

    private async Task RefreshDiagnosticsAsync()
    {
        try
        {
            var diagTask = App.AgentApi.GetDiagnosticsAsync();
            var logsTask = App.AgentApi.GetLogsAsync();
            await Task.WhenAll(diagTask, logsTask);
            var diag = await diagTask;
            var logs = await logsTask;
            if (diag is not null)
            {
                DiagControlText.Text = diag.Status.Connected ? $"{diag.Status.Transport.ToUpperInvariant()} · {diag.Status.LatencyMs} ms" : "未连接";
                var path = !string.IsNullOrWhiteSpace(diag.Status.DirectPath) ? diag.Status.DirectPath :
                           !string.IsNullOrWhiteSpace(diag.Status.P2PPath) ? diag.Status.P2PPath : "Relay / 未知";
                DiagPathText.Text = path;
                DiagConnectionsText.Text = diag.Connections.Count.ToString();

                var exitTcp = diag.Exit?.ActiveTcp ?? [];
                DiagExitTcpText.Text = exitTcp.Count.ToString();
                DiagExitTcpPanel.Children.Clear();
                if (exitTcp.Count == 0)
                {
                    DiagExitTcpPanel.Children.Add(new TextBlock
                    {
                        Text = "当前没有作为 Exit 承载的活跃 TCP 转发。",
                        Foreground = ThemeBrush("TextFillColorSecondaryBrush")
                    });
                }
                else
                {
                    foreach (var item in exitTcp.Take(8))
                    {
                        var target = string.IsNullOrWhiteSpace(item.Host) ? item.Remote : $"{item.Host}:{item.Port}";
                        var phase = $"tunnel→target {PumpPhaseLabel(item.TunnelToTarget.Phase)} · target→tunnel {PumpPhaseLabel(item.TargetToTunnel.Phase)}";
                        var traffic = $"↑ {FormatBytes(item.TunnelToTarget.ReadBytes)} · ↓ {FormatBytes(item.TargetToTunnel.ReadBytes)}";
                        var remote = string.IsNullOrWhiteSpace(item.Remote) ? "" : $" · client {item.Remote}";
                        DiagExitTcpPanel.Children.Add(TwoLine(target, $"{traffic} · {phase}{remote}"));
                    }

                    if (exitTcp.Count > 8)
                        DiagExitTcpPanel.Children.Add(new TextBlock
                        {
                            Text = $"另有 {exitTcp.Count - 8} 条活跃 Exit TCP 已包含在诊断数据中。",
                            Foreground = ThemeBrush("TextFillColorSecondaryBrush")
                        });
                }
            }
            _logCache = logs;
            RenderLogs();
            DiagnosticsBar.IsOpen = false;
        }
        catch (Exception ex) { ShowInfo(DiagnosticsBar, "诊断读取失败", ex.Message, InfoBarSeverity.Warning); }
    }

    private void LogSearch_Changed(object sender, TextChangedEventArgs e) => RenderLogs();

    private void RenderLogs()
    {
        if (LogsTextBox is null) return;
        var search = LogSearchBox?.Text?.Trim() ?? "";
        var lines = _logCache
            .Where(x => string.IsNullOrWhiteSpace(search) ||
                        x.Timestamp.Contains(search, StringComparison.CurrentCultureIgnoreCase) ||
                        x.Message.Contains(search, StringComparison.CurrentCultureIgnoreCase))
            .Select(x => $"{x.Timestamp} {x.Message}");
        LogsTextBox.Text = string.Join(Environment.NewLine, lines);
    }

    private void CopyLogs_Click(object sender, RoutedEventArgs e)
    {
        var package = new DataPackage();
        package.SetText(LogsTextBox.Text ?? "");
        Clipboard.SetContent(package);
        ShowInfo(DiagnosticsBar, "日志已复制", string.IsNullOrWhiteSpace(LogSearchBox.Text) ? "已复制当前日志。" : "已复制当前搜索结果。", InfoBarSeverity.Success);
    }

    private async void ExportLogs_Click(object sender, RoutedEventArgs e)
    {
        try
        {
            var downloads = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), "Downloads", "RelayProxy");
            Directory.CreateDirectory(downloads);
            var path = Path.Combine(downloads, $"relayproxy-logs-{DateTime.Now:yyyyMMdd-HHmmss}.txt");
            await File.WriteAllTextAsync(path, LogsTextBox.Text ?? "");
            ShowInfo(DiagnosticsBar, "日志已导出", path, InfoBarSeverity.Success);
            try
            {
                Process.Start(new ProcessStartInfo
                {
                    FileName = "explorer.exe",
                    ArgumentList = { "/select,", path },
                    UseShellExecute = true
                });
            }
            catch { }
        }
        catch (Exception ex)
        {
            ShowInfo(DiagnosticsBar, "导出日志失败", ex.Message, InfoBarSeverity.Error);
        }
    }

    private void CollectConnectionsDiagnostics_Click(object sender, RoutedEventArgs e)
    {
        SelectNavigation("diagnostics");
        CollectDiagnostics_Click(sender, e);
    }

    private async void CollectDiagnostics_Click(object sender, RoutedEventArgs e)
    {
        if (_diagnosticCollecting) return;
        _diagnosticCollecting = true;
        CollectDiagnosticsButton.IsEnabled = false;

        var temp = Path.Combine(Path.GetTempPath(), "RelayProxy-diagnostic-" + Guid.NewGuid().ToString("N"));
        try
        {
            Directory.CreateDirectory(temp);
            var samples = new List<DiagnosticsSnapshotDto>();
            const int sampleCount = 7;
            for (var index = 0; index < sampleCount; index++)
            {
                DiagnosticCollectionText.Text = $"正在采集诊断… {index + 1}/{sampleCount}（约 30 秒）";
                var snapshot = await App.AgentApi.GetDiagnosticsAsync();
                if (snapshot is not null) samples.Add(snapshot);
                if (index + 1 < sampleCount)
                    await Task.Delay(TimeSpan.FromSeconds(5));
            }

            DiagnosticCollectionText.Text = "正在整理日志与诊断包…";
            var logs = await App.AgentApi.GetLogsAsync();
            var manifest = new
            {
                format = "relayproxy-diagnostic-v1",
                collectedAt = DateTimeOffset.Now,
                durationSeconds = 30,
                sampleIntervalSeconds = 5,
                sampleCount = samples.Count,
                note = "此诊断包不包含 RelayProxy 配置文件、密码或管理 token。"
            };

            var jsonOptions = new JsonSerializerOptions { WriteIndented = true };
            await File.WriteAllTextAsync(Path.Combine(temp, "manifest.json"), JsonSerializer.Serialize(manifest, jsonOptions));
            await File.WriteAllTextAsync(Path.Combine(temp, "diagnostics.json"), JsonSerializer.Serialize(samples, jsonOptions));
            await File.WriteAllTextAsync(
                Path.Combine(temp, "logs.txt"),
                string.Join(Environment.NewLine, logs.Select(x => $"{x.Timestamp} {x.Message}")));

            var downloads = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), "Downloads", "RelayProxy");
            Directory.CreateDirectory(downloads);
            var zip = Path.Combine(downloads, $"relayproxy-diagnostic-{DateTime.Now:yyyyMMdd-HHmmss}.zip");
            if (File.Exists(zip)) File.Delete(zip);
            ZipFile.CreateFromDirectory(temp, zip, CompressionLevel.Optimal, includeBaseDirectory: false);

            DiagnosticCollectionText.Text = $"诊断包已保存：{zip}";
            ShowInfo(DiagnosticsBar, "诊断包采集完成", zip, InfoBarSeverity.Success);
            try
            {
                Process.Start(new ProcessStartInfo
                {
                    FileName = "explorer.exe",
                    ArgumentList = { "/select,", zip },
                    UseShellExecute = true
                });
            }
            catch { }
        }
        catch (Exception ex)
        {
            DiagnosticCollectionText.Text = "诊断采集失败，可重新尝试。";
            ShowInfo(DiagnosticsBar, "诊断采集失败", ex.Message, InfoBarSeverity.Error);
        }
        finally
        {
            CollectDiagnosticsButton.IsEnabled = true;
            _diagnosticCollecting = false;
            try { Directory.Delete(temp, recursive: true); } catch { }
        }
    }

    private async void SaveConnection_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var serverAddress = HostValue(ServerAddressBox, "Relay Server");
            var identityId = IdentityIdValue();
            var transport = ComboTag(TransportCombo, "auto");
            if (!TlsSwitch.IsOn && string.Equals(transport, "quic_only", StringComparison.OrdinalIgnoreCase))
                throw new InvalidOperationException("QUIC 需要开启 TLS；请启用 TLS，或改为 TCP/TLS。");

            var result = await App.AgentApi.SaveConfigAsync(new
            {
                revision = _config.Revision,
                server = new { address = serverAddress, quicPort = SafeInt(QuicPortBox, 443), tcpPort = SafeInt(TcpPortBox, 443), tlsEnabled = TlsSwitch.IsOn },
                device = new { name = DeviceNameBox.Text.Trim(), identityId },
                transport = TlsSwitch.IsOn ? transport : "tcp_only",
                p2p = new { enabled = P2PEnabledSwitch.IsOn, mode = ComboTag(P2PModeCombo, "auto"), punchTimeoutMs = SafeInt(PunchTimeoutBox, 3500), keepaliveSec = SafeInt(KeepaliveBox, 10), idleTimeoutSec = SafeInt(IdleTimeoutBox, 90), maxExitSessions = SafeInt(MaxSessionsBox, 4), fallback = P2PFallbackSwitch.IsOn },
                direct = new { @public = new { advertise = PublicAdvertiseBox.Text.Trim() } }
            });
            HandleSaveResult(ConnectionBar, result);
            _dirtyPages.Remove("connection");
            await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(ConnectionBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private void CopySocksAddress_Click(object sender, RoutedEventArgs e) =>
        CopyProxyAddress("socks5", SocksListenBox.Text, SafeInt(SocksPortBox, 1080));

    private void CopyHttpAddress_Click(object sender, RoutedEventArgs e) =>
        CopyProxyAddress("http", HttpListenBox.Text, SafeInt(HttpPortBox, 8080));

    private void CopyProxyAddress(string scheme, string listen, int port)
    {
        var host = listen.Trim();
        if (string.IsNullOrWhiteSpace(host) || host is "0.0.0.0" or "::" or "[::]")
            host = "127.0.0.1";
        if (host.Contains(':') && !host.StartsWith('['))
            host = $"[{host}]";
        var value = $"{scheme}://{host}:{port}";
        var package = new DataPackage();
        package.SetText(value);
        Clipboard.SetContent(package);
        ShowInfo(ProxyBar, "代理地址已复制", value, InfoBarSeverity.Success);
    }

    private async void SaveProxy_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var socksListen = HostValue(SocksListenBox, "SOCKS5 地址");
            var httpListen = HostValue(HttpListenBox, "HTTP 地址");
            var socksPort = SafeInt(SocksPortBox, 1080);
            var httpPort = SafeInt(HttpPortBox, 8080);
            if (SocksEnabledSwitch.IsOn && HttpEnabledSwitch.IsOn && string.Equals(socksListen, httpListen, StringComparison.OrdinalIgnoreCase) && socksPort == httpPort)
                throw new InvalidOperationException("SOCKS5 与 HTTP 不能监听同一个地址和端口。");
            var result = await App.AgentApi.SaveConfigAsync(new
            {
                revision = _config.Revision,
                proxy = new
                {
                    socks5Enabled = SocksEnabledSwitch.IsOn, socks5Listen, socks5Port = socksPort,
                    httpEnabled = HttpEnabledSwitch.IsOn, httpListen, httpPort
                },
                network = new { mode = TransparentProxySwitch.IsOn ? "divert" : "", excludeProcesses = SplitList(ExcludeProcessesBox.Text) }
            });
            HandleSaveResult(ProxyBar, result);
            _dirtyPages.Remove("proxy");
            await LoadConfigAsync();
            await RefreshNetworkServiceAsync();
        }
        catch (Exception ex) { ShowInfo(ProxyBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private void ExitUpstreamMode_Changed(object sender, SelectionChangedEventArgs e) => UpdateExitUpstreamFields();

    private void UpdateExitUpstreamFields()
    {
        var enabled = !string.IsNullOrWhiteSpace(ComboTag(ExitUpstreamModeCombo, ""));
        ExitUpstreamAddressBox.IsEnabled = enabled;
        ExitUpstreamUserBox.IsEnabled = enabled;
        ExitUpstreamPasswordBox.IsEnabled = enabled;
    }

    private async void SaveExitShare_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var upstreamMode = ComboTag(ExitUpstreamModeCombo, "");
            var upstreamAddress = ExitUpstreamAddressBox.Text.Trim();
            if (!string.IsNullOrWhiteSpace(upstreamMode))
                upstreamAddress = EndpointValue(ExitUpstreamAddressBox, "上游代理地址");

            var result = await App.AgentApi.SaveConfigAsync(new
            {
                revision = _config.Revision,
                exit = new
                {
                    enabled = ExitEnabledSwitch.IsOn,
                    allowInternet = AllowInternetCheck.IsChecked == true,
                    allowPrivateNetwork = AllowPrivateCheck.IsChecked == true,
                    allowLoopback = AllowLoopbackCheck.IsChecked == true,
                    upstream = new
                    {
                        mode = upstreamMode,
                        address = upstreamAddress,
                        username = ExitUpstreamUserBox.Text.Trim(),
                        password = ExitUpstreamPasswordBox.Password
                    },
                    access = new
                    {
                        mode = ComboTag(AccessModeCombo, ""),
                        domains = SplitList(AccessDomainsBox.Text),
                        cidrs = SplitList(AccessCidrsBox.Text)
                    }
                }
            });
            HandleSaveResult(ExitShareBar, result);
            _dirtyPages.Remove("exitshare");
            await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(ExitShareBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void DiscardRouting_Click(object sender, RoutedEventArgs e)
    {
        if (!_dirtyPages.Contains("routing"))
        {
            ShowInfo(RoutingBar, "没有未保存的修改", "", InfoBarSeverity.Informational);
            return;
        }

        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = "放弃分流规则修改",
            Content = "将恢复为当前已保存的路由模式、默认动作和规则顺序。",
            PrimaryButtonText = "放弃修改",
            CloseButtonText = "继续编辑",
            DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return;

        _dirtyPages.Remove("routing");
        await LoadConfigAsync();
        ShowInfo(RoutingBar, "已放弃修改", "表单已恢复为当前保存配置。", InfoBarSeverity.Success);
    }

    private async void SaveRouting_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var result = await App.AgentApi.SaveConfigAsync(new
            {
                revision = _config.Revision,
                routing = new { mode = ComboTag(RoutingModeCombo, "rule"), default_action = ComboTag(DefaultActionCombo, "PROXY"), rules = _routingRules }
            });
            HandleSaveResult(RoutingBar, result);
            _dirtyPages.Remove("routing");
            await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(RoutingBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void AddRule_Click(object sender, RoutedEventArgs e)
    {
        var rule = new RoutingRuleDto { Name = "新规则", Enabled = true, Action = "PROXY" };
        if (await EditRuleAsync(rule, isNew: true)) { _routingRules.Add(rule); MarkDirty("routing"); RenderRoutingRules(); }
    }

    private void RenderRoutingRules()
    {
        RoutingRulesPanel.Children.Clear();
        for (var index = 0; index < _routingRules.Count; index++)
        {
            var i = index;
            var rule = _routingRules[i];
            var grid = new Grid { ColumnSpacing = 10 };
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            grid.ColumnDefinitions.Add(new ColumnDefinition());
            grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            var enabled = new ToggleSwitch { IsOn = rule.Enabled, VerticalAlignment = VerticalAlignment.Center };
            enabled.Toggled += (_, _) => { rule.Enabled = enabled.IsOn; MarkDirty("routing"); };
            grid.Children.Add(enabled);
            var details = string.Join(" · ", new[]
            {
                rule.Action, string.IsNullOrWhiteSpace(rule.ExitId) ? "" : rule.ExitId,
                rule.Processes.Count > 0 ? $"进程 {string.Join(", ", rule.Processes)}" : "",
                rule.Targets.Count > 0 ? $"目标 {string.Join(", ", rule.Targets.Take(3))}" : "",
                rule.Protocols.Count > 0 ? string.Join("+", rule.Protocols.Select(x => x.ToUpperInvariant())) : ""
            }.Where(x => !string.IsNullOrWhiteSpace(x)));
            var text = TwoLine($"{i + 1}. {rule.Name}", details);
            Grid.SetColumn(text, 1); grid.Children.Add(text);
            var ops = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 5, VerticalAlignment = VerticalAlignment.Center };
            var up = new Button { Content = "↑", IsEnabled = i > 0 };
            var down = new Button { Content = "↓", IsEnabled = i < _routingRules.Count - 1 };
            var edit = new Button { Content = "编辑" };
            var del = new Button { Content = "删除" };
            up.Click += (_, _) => { (_routingRules[i - 1], _routingRules[i]) = (_routingRules[i], _routingRules[i - 1]); MarkDirty("routing"); RenderRoutingRules(); };
            down.Click += (_, _) => { (_routingRules[i + 1], _routingRules[i]) = (_routingRules[i], _routingRules[i + 1]); MarkDirty("routing"); RenderRoutingRules(); };
            edit.Click += async (_, _) => { if (await EditRuleAsync(rule, false)) { MarkDirty("routing"); RenderRoutingRules(); } };
            del.Click += async (_, _) =>
            {
                var dialog = new ContentDialog { XamlRoot = Content.XamlRoot, Title = "删除路由规则", Content = $"确定删除“{rule.Name}”吗？", PrimaryButtonText = "删除", CloseButtonText = "取消", DefaultButton = ContentDialogButton.Close };
                if (await dialog.ShowAsync() == ContentDialogResult.Primary) { _routingRules.Remove(rule); MarkDirty("routing"); RenderRoutingRules(); }
            };
            ops.Children.Add(up); ops.Children.Add(down); ops.Children.Add(edit); ops.Children.Add(del);
            Grid.SetColumn(ops, 2); grid.Children.Add(ops);

            var card = Card(grid);
            card.Tag = i;
            card.CanDrag = true;
            card.AllowDrop = true;
            ToolTipService.SetToolTip(card, "拖动卡片可调整规则顺序");
            card.DragStarting += (_, args) =>
            {
                args.Data.SetText(i.ToString());
                args.Data.RequestedOperation = DataPackageOperation.Move;
            };
            card.DragOver += (_, args) =>
            {
                args.AcceptedOperation = DataPackageOperation.Move;
                args.DragUIOverride.Caption = "移动规则";
                args.DragUIOverride.IsCaptionVisible = true;
            };
            card.Drop += async (_, args) =>
            {
                try
                {
                    var value = await args.DataView.GetTextAsync();
                    if (!int.TryParse(value, out var source) || source < 0 || source >= _routingRules.Count) return;
                    var target = i;
                    if (source == target) return;

                    var moving = _routingRules[source];
                    _routingRules.RemoveAt(source);
                    if (source < target) target--;
                    target = Math.Clamp(target, 0, _routingRules.Count);
                    _routingRules.Insert(target, moving);
                    MarkDirty("routing");
                    RenderRoutingRules();
                }
                catch { }
            };
            RoutingRulesPanel.Children.Add(card);
        }
        if (_routingRules.Count == 0)
            RoutingRulesPanel.Children.Add(Card(new TextBlock { Text = "暂无规则。按规则分流模式下将使用“未命中时”动作。", Foreground = ThemeBrush("TextFillColorSecondaryBrush") }));
    }

    private async Task<bool> EditRuleAsync(RoutingRuleDto rule, bool isNew)
    {
        var name = new TextBox { Header = "规则名称", Text = rule.Name };
        var processes = new TextBox { Header = "应用 / 进程（每行或逗号分隔）", Text = string.Join(Environment.NewLine, rule.Processes), AcceptsReturn = true, MinHeight = 70 };
        var targets = new TextBox { Header = "域名 / IP / CIDR", Text = string.Join(Environment.NewLine, rule.Targets), AcceptsReturn = true, MinHeight = 70 };
        var ports = new TextBox { Header = "端口", Text = string.Join(", ", rule.Ports), PlaceholderText = "443, 8000-8999" };
        var protocols = new TextBox { Header = "协议", Text = string.Join(", ", rule.Protocols), PlaceholderText = "tcp, udp" };
        var action = new ComboBox { Header = "动作", HorizontalAlignment = HorizontalAlignment.Stretch };
        action.Items.Add(new ComboBoxItem { Content = "代理", Tag = "PROXY" }); action.Items.Add(new ComboBoxItem { Content = "本机直连", Tag = "DIRECT" }); action.Items.Add(new ComboBoxItem { Content = "阻断", Tag = "REJECT" });
        SelectComboTag(action, rule.Action);
        var exits = await App.AgentApi.GetExitsAsync();
        var exit = new ComboBox { Header = "指定出口（可选）", HorizontalAlignment = HorizontalAlignment.Stretch };
        exit.Items.Add(new ComboBoxItem { Content = "自动选择 / 使用默认出口", Tag = "" });
        foreach (var candidate in exits.OrderByDescending(x => x.Online).ThenBy(x => x.Name))
        {
            var label = string.IsNullOrWhiteSpace(candidate.Name) ? candidate.DeviceId : candidate.Name;
            exit.Items.Add(new ComboBoxItem
            {
                Content = $"{label} · {(candidate.Online ? "在线" : "离线")} · {candidate.DeviceId}",
                Tag = candidate.DeviceId
            });
        }
        if (!string.IsNullOrWhiteSpace(rule.ExitId) &&
            !exit.Items.OfType<ComboBoxItem>().Any(x => string.Equals(x.Tag?.ToString(), rule.ExitId, StringComparison.OrdinalIgnoreCase)))
        {
            exit.Items.Add(new ComboBoxItem
            {
                Content = $"当前配置 · 已不在授权列表 · {rule.ExitId}",
                Tag = rule.ExitId
            });
        }
        SelectComboTag(exit, rule.ExitId);
        var datagram = new ToggleSwitch { Header = "必须使用原生数据报", IsOn = rule.DatagramRequired };
        var handleDirect = new ToggleSwitch { Header = "由 RelayProxy 处理 DIRECT", IsOn = rule.HandleDirect };
        var panel = new StackPanel { Spacing = 10 };
        foreach (var control in new UIElement[] { name, processes, targets, ports, protocols, action, exit, datagram, handleDirect }) panel.Children.Add(control);
        var dialog = new ContentDialog { XamlRoot = Content.XamlRoot, Title = isNew ? "添加路由规则" : "编辑路由规则", Content = new ScrollViewer { Content = panel, MaxHeight = 560 }, PrimaryButtonText = "确定", CloseButtonText = "取消", DefaultButton = ContentDialogButton.Primary };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return false;
        if (string.IsNullOrWhiteSpace(name.Text)) { ShowInfo(RoutingBar, "规则名称不能为空", "", InfoBarSeverity.Warning); return false; }
        rule.Name = name.Text.Trim(); rule.Processes = SplitList(processes.Text); rule.Targets = SplitList(targets.Text); rule.Ports = SplitList(ports.Text);
        rule.Protocols = SplitList(protocols.Text).Select(x => x.ToLowerInvariant()).ToList(); rule.Action = ComboTag(action, "PROXY"); rule.ExitId = ComboTag(exit, "");
        rule.DatagramRequired = datagram.IsOn; rule.HandleDirect = handleDirect.IsOn;
        return true;
    }

    private async void ReloadConfig_Click(object sender, RoutedEventArgs e)
    {
        if (!await ConfirmDiscardChangesAsync("重新读取配置")) return;
        _dirtyPages.Clear();
        try { var result = await App.AgentApi.ReloadConfigAsync(); HandleSaveResult(ConnectionBar, result); await LoadConfigAsync(); }
        catch (Exception ex) { ShowInfo(ConnectionBar, "重新读取失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void RefreshNetworkService_Click(object sender, RoutedEventArgs e) => await RefreshNetworkServiceAsync();

    private async void RepairNetworkService_Click(object sender, RoutedEventArgs e)
    {
        var confirm = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = "安装 / 修复 RelayProxy Network Service",
            Content = "Windows 透明代理需要一次管理员授权来安装或修复受保护的 Network Service 与 WinDivert 驱动。\n\n日常启动 RelayProxy GUI 不需要管理员权限；只有安装、修复或卸载服务时才会出现 UAC。",
            PrimaryButtonText = "继续并请求管理员权限",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Close
        };
        if (await confirm.ShowAsync() != ContentDialogResult.Primary) return;

        try
        {
            RepairNetworkServiceButton.IsEnabled = false;
            var result = await App.AgentApi.RepairNetworkServiceAsync();
            ShowInfo(ProxyBar, "Network Service 已就绪", result?.Message ?? "服务已安装/修复并启动。", InfoBarSeverity.Success);
            await RefreshNetworkServiceAsync();
        }
        catch (Exception ex)
        {
            ShowInfo(ProxyBar, "安装 / 修复失败", ex.Message, InfoBarSeverity.Error);
            await RefreshNetworkServiceAsync();
        }
    }

    private async void UninstallNetworkService_Click(object sender, RoutedEventArgs e)
    {
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = "卸载 RelayProxy Network Service",
            Content = "卸载后会同时关闭系统透明代理配置。SOCKS5 和 HTTP 代理不会受影响。",
            PrimaryButtonText = "卸载",
            CloseButtonText = "取消",
            DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return;

        try
        {
            UninstallNetworkServiceButton.IsEnabled = false;
            var result = await App.AgentApi.UninstallNetworkServiceAsync();
            _suppressDirtyTracking = true;
            TransparentProxySwitch.IsOn = false;
            _suppressDirtyTracking = false;
            _dirtyPages.Remove("proxy");
            ShowInfo(ProxyBar, "Network Service 已卸载", result?.Message ?? "透明代理服务已移除。", result?.RebootCleanup == true ? InfoBarSeverity.Warning : InfoBarSeverity.Success);
            await LoadConfigAsync();
            await RefreshNetworkServiceAsync();
        }
        catch (Exception ex)
        {
            ShowInfo(ProxyBar, "卸载失败", ex.Message, InfoBarSeverity.Error);
            await RefreshNetworkServiceAsync();
        }
    }

    private async void RefreshDevices_Click(object sender, RoutedEventArgs e) => await RefreshDevicesAsync();
    private async void RefreshRdp_Click(object sender, RoutedEventArgs e) => await RefreshRdpAsync();
    private async void RefreshDiagnostics_Click(object sender, RoutedEventArgs e) => await RefreshDiagnosticsAsync();
    private async void ClearLogs_Click(object sender, RoutedEventArgs e)
    {
        try { await App.AgentApi.ClearLogsAsync(); _logCache.Clear(); RenderLogs(); ShowInfo(DiagnosticsBar, "日志已清空", "", InfoBarSeverity.Success); }
        catch (Exception ex) { ShowInfo(DiagnosticsBar, "清空失败", ex.Message, InfoBarSeverity.Error); }
    }
    private async void DisconnectRdp_Click(object sender, RoutedEventArgs e)
    {
        try { await App.AgentApi.DisconnectRdpAsync(); ShowInfo(RdpBar, "RDP 已断开", "", InfoBarSeverity.Success); await RefreshRdpAsync(); }
        catch (Exception ex) { ShowInfo(RdpBar, "断开失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void AutostartSwitch_Toggled(object sender, RoutedEventArgs e)
    {
        if (_suppressAutostart || !App.AgentApi.IsReady) return;
        try { await App.AgentApi.SetAutostartAsync(AutostartSwitch.IsOn); ShowInfo(SettingsBar, "开机自启已更新", AutostartSwitch.IsOn ? "已启用" : "已关闭", InfoBarSeverity.Success); }
        catch (Exception ex)
        {
            _suppressAutostart = true; AutostartSwitch.IsOn = !AutostartSwitch.IsOn; _suppressAutostart = false;
            ShowInfo(SettingsBar, "设置失败", ex.Message, InfoBarSeverity.Error);
        }
    }

    private async void SaveGuiSettings_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var theme = ComboTag(ThemeCombo, "system");
            var result = await App.AgentApi.SaveConfigAsync(new { revision = _config.Revision, gui = new { minimizeToTray = MinimizeToTraySwitch.IsOn, systemNotifications = SystemNotificationsSwitch.IsOn, theme, verificationPopupTimeoutSec = SafeInt(PopupTimeoutBox, 15) } });
            ApplyTheme(theme);
            HandleSaveResult(SettingsBar, result); _dirtyPages.Remove("settings"); await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(SettingsBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void ExitRelayProxy_Click(object sender, RoutedEventArgs e) => await RequestShutdownAsync();

    private async void RestartAgent_Click(object sender, RoutedEventArgs e)
    {
        if (!await ConfirmDiscardChangesAsync("重启 Agent")) return;
        _dirtyPages.Clear();
        RestartAgentButton.IsEnabled = false;
        var original = RestartAgentButton.Content;
        RestartAgentButton.Content = "正在重启…";
        try
        {
            await App.AgentHost.RestartAsync();
            await LoadConfigAsync();
            await RefreshCurrentAsync();
            RestartAgentButton.Visibility = Visibility.Collapsed;
        }
        catch (Exception ex)
        {
            var dialog = new ContentDialog
            {
                XamlRoot = Content.XamlRoot,
                Title = "Agent 重启失败",
                Content = ex.Message,
                CloseButtonText = "关闭"
            };
            await dialog.ShowAsync();
        }
        finally
        {
            RestartAgentButton.Content = original;
            RestartAgentButton.IsEnabled = true;
        }
    }

    private async void PauseProxy_Click(object sender, RoutedEventArgs e) => await SetProxyPausedAsync(!_proxyPaused, showFeedback: true);

    private async Task SetProxyPausedAsync(bool paused, bool showFeedback)
    {
        PauseProxyButton.IsEnabled = false;
        try
        {
            var status = await App.AgentApi.SetProxyPausedAsync(paused);
            _proxyPaused = status?.ProxyPaused ?? paused;
            await RefreshOverviewAsync();
            if (showFeedback)
            {
                ShowInfo(
                    OverviewBar,
                    _proxyPaused ? "代理已暂停" : "代理已恢复",
                    _proxyPaused
                        ? "新的透明代理 PROXY 流量会临时直连；SOCKS5 / HTTP 的新代理请求会明确拒绝。现有连接自然结束，控制连接、消息、RDP 与 Exit 不受影响。"
                        : "新的代理流量已恢复按当前路由和出口策略处理。",
                    InfoBarSeverity.Success);
            }
        }
        catch (Exception ex)
        {
            if (showFeedback)
                ShowInfo(OverviewBar, paused ? "暂停代理失败" : "恢复代理失败", ex.Message, InfoBarSeverity.Error);
            else
                _tray?.UpdateTooltip($"RelayProxy · {(paused ? "暂停失败" : "恢复失败")}");
        }
        finally
        {
            PauseProxyButton.IsEnabled = App.AgentApi.IsReady;
        }
    }

    private async void RefreshOverview_Click(object sender, RoutedEventArgs e) => await RefreshOverviewAsync();
    private async void RefreshExits_Click(object sender, RoutedEventArgs e) => await RefreshExitsAsync();
    private async void RefreshConnections_Click(object sender, RoutedEventArgs e) => await RefreshConnectionsAsync();
    private async void ClearConnections_Click(object sender, RoutedEventArgs e)
    {
        try
        {
            await App.AgentApi.ClearConnectionsAsync();
            await RefreshConnectionsAsync();
        }
        catch (Exception ex)
        {
            var dialog = new ContentDialog
            {
                XamlRoot = Content.XamlRoot,
                Title = "清空连接记录失败",
                Content = ex.Message,
                CloseButtonText = "关闭"
            };
            await dialog.ShowAsync();
        }
    }

    private void OpenConfigDirectory_Click(object sender, RoutedEventArgs e)
    {
        var path = _config?.ConfigPath;
        if (string.IsNullOrWhiteSpace(path) || path == "(未指定)") return;
        var directory = Path.GetDirectoryName(path);
        if (string.IsNullOrWhiteSpace(directory) || !Directory.Exists(directory)) return;
        try
        {
            Process.Start(new ProcessStartInfo
            {
                FileName = "explorer.exe",
                ArgumentList = { directory },
                UseShellExecute = true
            });
        }
        catch (Exception ex)
        {
            ShowInfo(SettingsBar, "无法打开配置目录", ex.Message, InfoBarSeverity.Error);
        }
    }

    private async void RefreshMessages_Click(object sender, RoutedEventArgs e) => await RefreshMessagesAsync();
    private async void ClearMessages_Click(object sender, RoutedEventArgs e)
    {
        try { await App.AgentApi.ClearMessagesAsync(); _messageCache.Clear(); MessagesPanel.Children.Clear(); ShowInfo(MessagesBar, "消息历史已清空", "", InfoBarSeverity.Success); }
        catch (Exception ex) { ShowInfo(MessagesBar, "清空失败", ex.Message, InfoBarSeverity.Error); }
    }
    private void OpenExits_Click(object sender, RoutedEventArgs e) => SelectNavigation("exits");
    private void OpenConnections_Click(object sender, RoutedEventArgs e) => SelectNavigation("connections");

    private void SelectNavigation(string tag)
    {
        var item = FindNavigationItem(tag);
        if (item is not null)
            Navigation.SelectedItem = item;
        else
            ShowPage(tag);
    }

    private NavigationViewItem? FindNavigationItem(string tag) =>
        Navigation.MenuItems.OfType<NavigationViewItem>()
            .Concat(Navigation.FooterMenuItems.OfType<NavigationViewItem>())
            .FirstOrDefault(x => string.Equals(x.Tag as string, tag, StringComparison.Ordinal));

    private void RestoreNavigationSelection(string tag)
    {
        var item = FindNavigationItem(tag);
        if (item is null) return;
        _suppressNavigationSelection = true;
        Navigation.SelectedItem = item;
        _suppressNavigationSelection = false;
    }

    private void RegisterDirtyTracking()
    {
        TrackDirty("connection", ServerAddressBox, QuicPortBox, TcpPortBox, TlsSwitch, DeviceNameBox, IdentityBox, TransportCombo,
            P2PEnabledSwitch, P2PModeCombo, P2PFallbackSwitch, PublicAdvertiseBox, PunchTimeoutBox, KeepaliveBox, IdleTimeoutBox, MaxSessionsBox);
        TrackDirty("proxy", SocksEnabledSwitch, SocksListenBox, SocksPortBox, HttpEnabledSwitch, HttpListenBox, HttpPortBox,
            TransparentProxySwitch, ExcludeProcessesBox);
        TrackDirty("exitshare", ExitEnabledSwitch, AllowInternetCheck, AllowPrivateCheck, AllowLoopbackCheck, ExitUpstreamModeCombo,
            ExitUpstreamAddressBox, ExitUpstreamUserBox, ExitUpstreamPasswordBox, AccessModeCombo, AccessDomainsBox, AccessCidrsBox);
        TrackDirty("routing", RoutingModeCombo, DefaultActionCombo);
        TrackDirty("settings", MinimizeToTraySwitch, SystemNotificationsSwitch, ThemeCombo, PopupTimeoutBox);
    }

    private void TrackDirty(string page, params FrameworkElement[] controls)
    {
        foreach (var control in controls)
        {
            switch (control)
            {
                case TextBox text:
                    text.TextChanged += (_, _) => MarkDirty(page);
                    break;
                case PasswordBox password:
                    password.PasswordChanged += (_, _) => MarkDirty(page);
                    break;
                case ToggleSwitch toggle:
                    toggle.Toggled += (_, _) => MarkDirty(page);
                    break;
                case CheckBox check:
                    check.Checked += (_, _) => MarkDirty(page);
                    check.Unchecked += (_, _) => MarkDirty(page);
                    break;
                case ComboBox combo:
                    combo.SelectionChanged += (_, _) => MarkDirty(page);
                    break;
                case NumberBox number:
                    number.ValueChanged += (_, _) => MarkDirty(page);
                    break;
            }
        }
    }

    private void MarkDirty(string page)
    {
        if (_suppressDirtyTracking || _config is null) return;
        if (!_dirtyPages.Add(page)) return;
        var bar = PageInfoBar(page);
        if (bar is not null)
            ShowInfo(bar, "有未保存的修改", "保存后生效；离开此页面或退出时会要求确认。", InfoBarSeverity.Warning);
    }

    private InfoBar? PageInfoBar(string page) => page switch
    {
        "connection" => ConnectionBar,
        "proxy" => ProxyBar,
        "exitshare" => ExitShareBar,
        "routing" => RoutingBar,
        "messages" => MessagesBar,
        "settings" => SettingsBar,
        _ => null
    };

    private async Task<bool> ConfirmDiscardChangesAsync(string action)
    {
        if (!_dirtyPages.Contains(_page)) return true;
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = "有未保存的修改",
            Content = $"{PageLabel(_page)}中的修改尚未保存。要{action}并放弃这些修改吗？",
            PrimaryButtonText = "放弃修改",
            CloseButtonText = "继续编辑",
            DefaultButton = ContentDialogButton.Close
        };
        return await dialog.ShowAsync() == ContentDialogResult.Primary;
    }

    private static string PageLabel(string page) => page switch
    {
        "connection" => "连接与路径",
        "proxy" => "本机代理",
        "exitshare" => "本机出口共享",
        "routing" => "分流规则",
        "messages" => "消息规则",
        "settings" => "设置",
        _ => "当前页面"
    };

    private void HandleSaveResult(InfoBar bar, SaveResultDto? result)
    {
        if (result is null) { ShowInfo(bar, "保存完成", "", InfoBarSeverity.Success); return; }
        if (_config is not null && !string.IsNullOrWhiteSpace(result.Revision)) _config.Revision = result.Revision;
        RestartAgentButton.Visibility = result.RestartRequired && App.AgentHost.CanRestart ? Visibility.Visible : Visibility.Collapsed;
        var details = result.RestartRequired && result.RestartFields.Count > 0 ? $"需要重启：{string.Join("、", result.RestartFields)}" : result.Message;
        if (result.RestartRequired && !App.AgentHost.CanRestart)
            details += " 当前连接的是外部 Agent，请手动重启该 Agent。";
        ShowInfo(bar, result.RestartRequired ? "设置已保存，需要重启" : "设置已应用", details, result.RestartRequired ? InfoBarSeverity.Warning : InfoBarSeverity.Success);
    }

    private void ApplyTheme(string? mode)
    {
        if (Content is not FrameworkElement root) return;
        root.RequestedTheme = mode?.ToLowerInvariant() switch
        {
            "light" => ElementTheme.Light,
            "dark" => ElementTheme.Dark,
            _ => ElementTheme.Default
        };
    }

    private static RoutingRuleDto CloneRule(RoutingRuleDto r) => new()
    {
        Name = r.Name, Enabled = r.Enabled, Action = r.Action, ExitId = r.ExitId, DatagramRequired = r.DatagramRequired, HandleDirect = r.HandleDirect,
        Processes = [.. r.Processes], Targets = [.. r.Targets], Ports = [.. r.Ports], Protocols = [.. r.Protocols]
    };
    private static List<string> SplitList(string value) => value.Split(new[] { '\r', '\n', ',' }, StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries).Distinct(StringComparer.OrdinalIgnoreCase).ToList();

    private static string HostValue(TextBox box, string label)
    {
        var host = box.Text.Trim();
        if (host.Length >= 2 && host[0] == '[' && host[^1] == ']')
            host = host[1..^1];

        var colonCount = host.Count(ch => ch == ':');
        if (string.IsNullOrWhiteSpace(host) || host.Any(char.IsWhiteSpace) || host.Contains('/') || host.Contains('\\') || colonCount == 1)
            throw new InvalidOperationException($"{label}请填写主机名或 IP，协议和端口请分开填写。");

        return host;
    }

    private string IdentityIdValue()
    {
        var value = IdentityBox.Text.Trim().ToLowerInvariant();
        var valid = value.Length == 16
            && value.All(ch => ch is >= 'a' and <= 'z' or >= '0' and <= '9')
            && value.Any(ch => ch is >= 'a' and <= 'z')
            && value.Any(char.IsDigit);
        if (!valid)
            throw new InvalidOperationException("Identity ID 必须是服务端生成的 16 位小写字母数字组合。");

        IdentityBox.Text = value;
        return value;
    }

    private static string EndpointValue(TextBox box, string label)
    {
        var value = box.Text.Trim();
        if (string.IsNullOrWhiteSpace(value) || value.Contains("://") || value.Any(char.IsWhiteSpace))
            throw new InvalidOperationException($"{label}必须使用 host:port 格式。");

        string host;
        string portText;
        if (value.StartsWith('['))
        {
            var close = value.IndexOf(']');
            if (close <= 1 || close + 2 >= value.Length || value[close + 1] != ':')
                throw new InvalidOperationException($"{label}必须使用 host:port 格式；IPv6 请使用 [addr]:port。");
            host = value[1..close];
            portText = value[(close + 2)..];
        }
        else
        {
            var colon = value.LastIndexOf(':');
            if (colon <= 0 || value.IndexOf(':') != colon)
                throw new InvalidOperationException($"{label}必须使用 host:port 格式；IPv6 请使用 [addr]:port。");
            host = value[..colon];
            portText = value[(colon + 1)..];
        }

        if (string.IsNullOrWhiteSpace(host) || host.Contains('/') || host.Contains('\\') || !int.TryParse(portText, out var port) || port is < 1 or > 65535)
            throw new InvalidOperationException($"{label}必须使用有效的 host:port，端口范围 1-65535。");

        return value;
    }

    private static int SafeInt(NumberBox box, int fallback) => double.IsNaN(box.Value) ? fallback : (int)Math.Round(box.Value);
    private static string ComboTag(ComboBox combo, string fallback) => (combo.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? fallback;
    private static void SelectComboTag(ComboBox combo, string? tag)
    {
        var found = combo.Items.OfType<ComboBoxItem>().FirstOrDefault(x => string.Equals(x.Tag?.ToString(), tag, StringComparison.OrdinalIgnoreCase));
        combo.SelectedItem = found ?? combo.Items.OfType<ComboBoxItem>().FirstOrDefault();
    }
    private static Border Card(UIElement child) => new() { Style = (Style)Application.Current.Resources["CardStyle"], Child = child };
    private static StackPanel TwoLine(string title, string subtitle)
    {
        var s = new StackPanel { Spacing = 2 };
        s.Children.Add(new TextBlock { Text = title, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap });
        s.Children.Add(new TextBlock { Text = subtitle, Foreground = ThemeBrush("TextFillColorSecondaryBrush"), TextWrapping = TextWrapping.Wrap });
        return s;
    }
    private static Brush ThemeBrush(string key) => (Brush)Application.Current.Resources[key];
    private static void ShowInfo(InfoBar bar, string title, string message, InfoBarSeverity severity) { bar.Title = title; bar.Message = message; bar.Severity = severity; bar.IsOpen = true; }
    private static string StateLabel(string value) => value switch { "connected" or "active" or "direct" => "已直连", "connecting" or "negotiating" or "punching" => "协商中", "cooldown" => "冷却中", "fallback" => "已降级", "disabled" => "已关闭", "unavailable" => "不可用", _ => string.IsNullOrWhiteSpace(value) ? "—" : value };
    private static string FormatRate(double n) => n < 1024 ? $"{n:0} B/s" : n < 1024 * 1024 ? $"{n / 1024:0.0} KB/s" : $"{n / 1024 / 1024:0.0} MB/s";
    private static string FormatBytes(ulong n) => n < 1024 ? $"{n} B" : n < 1024 * 1024 ? $"{n / 1024.0:0.0} KB" : n < 1024UL * 1024 * 1024 ? $"{n / 1024.0 / 1024:0.0} MB" : $"{n / 1024.0 / 1024 / 1024:0.0} GB";
    private static string PumpPhaseLabel(string value) => value switch { "read" => "读取", "write" => "写入", _ => "空闲" };
    private static string FormatCreatedAt(long value)
    {
        if (value <= 0) return "";
        try { return (value > 10_000_000_000 ? DateTimeOffset.FromUnixTimeMilliseconds(value) : DateTimeOffset.FromUnixTimeSeconds(value)).ToLocalTime().ToString("g"); }
        catch { return ""; }
    }
    private static string MessageTypeValue(PushMessageDto m)
    {
        var type = !string.IsNullOrWhiteSpace(m.MessageType) ? m.MessageType : m.PopupType;
        if (string.IsNullOrWhiteSpace(type) && !string.IsNullOrWhiteSpace(m.VerificationCode)) type = "verification_code";
        return string.IsNullOrWhiteSpace(type) ? "message" : type;
    }

    private static string PopupTypeLabel(PushMessageDto m)
    {
        return MessageTypeValue(m) switch { "verification_code" => "验证码", "important" => "重要提醒", _ => "普通消息" };
    }
}
