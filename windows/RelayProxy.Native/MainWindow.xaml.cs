using System.Diagnostics;
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
    private AgentConfigDto? _config;
    private List<RoutingRuleDto> _routingRules = [];
    private MessagePopupWindow? _popupWindow;
    private TrayIconService? _tray;
    private bool _messageBaselineReady;
    private bool _suppressAutostart;
    private bool _forceExit;
    private bool _shutdownInProgress;
    private string _lastDeviceId = "";
    private string _page = "overview";

    public MainWindow()
    {
        InitializeComponent();
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
        App.AgentHost.StateChanged -= OnAgentStateChanged;
        AppWindow.Closing -= OnAppWindowClosing;
        AppWindow.Changed -= OnAppWindowChanged;
        try { _popupWindow?.ClosePermanently(); } catch { }
        _tray?.Dispose();
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

        _ = ShutdownAndCloseAsync();
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

    private void OnTrayExitRequested() => DispatcherQueue.TryEnqueue(() => _ = ShutdownAndCloseAsync());

    private void Navigation_SelectionChanged(NavigationView sender, NavigationViewSelectionChangedEventArgs args)
    {
        if (args.SelectedItemContainer?.Tag is string tag) ShowPage(tag);
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
            ConnectionMetaText.Text = status.Connected ? $"{status.Transport.ToUpperInvariant()} · {status.DeviceName} · {status.IdentityName}" : "正在连接 Relay Server";
            ApprovalText.Text = status.ApprovalState switch { "approved" => "已审批", "pending" => "待审批", "rejected" => "已拒绝", "revoked" => "已撤销", _ => "未知" };
            _lastDeviceId = status.DeviceId;
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
            await PollMessagesAsync(render: false);
        }
        catch (Exception ex)
        {
            OverviewBar.Message = ex.Message;
            OverviewBar.IsOpen = true;
        }
    }

    private async Task LoadConfigAsync()
    {
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
    }

    private async Task RefreshExitsAsync()
    {
        try
        {
            var exits = await App.AgentApi.GetExitsAsync();
            ExitsPanel.Children.Clear();
            foreach (var exit in exits.OrderByDescending(x => x.Online).ThenBy(x => x.Name))
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
                        var result = await App.AgentApi.RunSpeedTestAsync(exit.DeviceId, 2);
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

    private async Task RefreshMessagesAsync() => await PollMessagesAsync(render: true);

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
                        _popupWindow ??= new MessagePopupWindow();
                        _popupWindow.EnqueueMessage(m, _config?.VerificationPopupTimeoutSec ?? 15);
                    }
                }
            }

            if (!render) return;
            MessagesPanel.Children.Clear(); MessagesBar.IsOpen = false;
            foreach (var m in messages.OrderByDescending(x => x.CreatedAt).Take(200))
            {
                var grid = new Grid { ColumnSpacing = 12 };
                grid.ColumnDefinitions.Add(new ColumnDefinition()); grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                var type = PopupTypeLabel(m);
                var when = FormatCreatedAt(m.CreatedAt);
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
                button.Click += async (_, _) =>
                {
                    try
                    {
                        await App.AgentApi.ConnectRdpAsync(target.DeviceId, autoLaunch: true);
                        ShowInfo(RdpBar, "RDP 已建立", "已创建本地回环入口并启动 mstsc.exe。", InfoBarSeverity.Success);
                        await RefreshRdpAsync();
                    }
                    catch (Exception ex) { ShowInfo(RdpBar, "RDP 连接失败", ex.Message, InfoBarSeverity.Error); }
                };
                Grid.SetColumn(button, 1); grid.Children.Add(button);
                RdpTargetsPanel.Children.Add(Card(grid));
            }
            if (targets.Count == 0) ShowInfo(RdpBar, "暂无授权设备", "服务端未向当前 Agent 授权 RDP 目标。", InfoBarSeverity.Informational);
        }
        catch (Exception ex) { ShowInfo(RdpBar, "RDP 状态不可用", ex.Message, InfoBarSeverity.Warning); }
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
            }
            LogsTextBox.Text = string.Join(Environment.NewLine, logs.Select(x => $"{x.Timestamp} {x.Message}"));
            DiagnosticsBar.IsOpen = false;
        }
        catch (Exception ex) { ShowInfo(DiagnosticsBar, "诊断读取失败", ex.Message, InfoBarSeverity.Warning); }
    }

    private async void SaveConnection_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var result = await App.AgentApi.SaveConfigAsync(new
            {
                revision = _config.Revision,
                server = new { address = ServerAddressBox.Text.Trim(), quicPort = SafeInt(QuicPortBox, 443), tcpPort = SafeInt(TcpPortBox, 443), tlsEnabled = TlsSwitch.IsOn },
                device = new { name = DeviceNameBox.Text.Trim(), identityId = IdentityBox.Text.Trim() },
                transport = ComboTag(TransportCombo, "auto"),
                p2p = new { enabled = P2PEnabledSwitch.IsOn, mode = ComboTag(P2PModeCombo, "auto"), punchTimeoutMs = SafeInt(PunchTimeoutBox, 3500), idleTimeoutSec = SafeInt(IdleTimeoutBox, 90), maxExitSessions = SafeInt(MaxSessionsBox, 4), fallback = P2PFallbackSwitch.IsOn },
                direct = new { @public = new { advertise = PublicAdvertiseBox.Text.Trim() } }
            });
            HandleSaveResult(ConnectionBar, result);
            await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(ConnectionBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void SaveProxy_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
            var socksPort = SafeInt(SocksPortBox, 1080);
            var httpPort = SafeInt(HttpPortBox, 8080);
            if (SocksEnabledSwitch.IsOn && HttpEnabledSwitch.IsOn && SocksListenBox.Text.Trim() == HttpListenBox.Text.Trim() && socksPort == httpPort)
                throw new InvalidOperationException("SOCKS5 与 HTTP 不能监听同一个地址和端口。");
            var result = await App.AgentApi.SaveConfigAsync(new
            {
                revision = _config.Revision,
                proxy = new
                {
                    socks5Enabled = SocksEnabledSwitch.IsOn, socks5Listen = SocksListenBox.Text.Trim(), socks5Port = socksPort,
                    httpEnabled = HttpEnabledSwitch.IsOn, httpListen = HttpListenBox.Text.Trim(), httpPort
                },
                network = new { mode = TransparentProxySwitch.IsOn ? "divert" : "", excludeProcesses = SplitList(ExcludeProcessesBox.Text) }
            });
            HandleSaveResult(ProxyBar, result);
            await LoadConfigAsync();
            await RefreshNetworkServiceAsync();
        }
        catch (Exception ex) { ShowInfo(ProxyBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void SaveExitShare_Click(object sender, RoutedEventArgs e)
    {
        if (_config is null) { await LoadConfigAsync(); if (_config is null) return; }
        try
        {
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
                        mode = ComboTag(ExitUpstreamModeCombo, ""),
                        address = ExitUpstreamAddressBox.Text.Trim(),
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
            await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(ExitShareBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
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
            await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(RoutingBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void AddRule_Click(object sender, RoutedEventArgs e)
    {
        var rule = new RoutingRuleDto { Name = "新规则", Enabled = true, Action = "PROXY" };
        if (await EditRuleAsync(rule, isNew: true)) { _routingRules.Add(rule); RenderRoutingRules(); }
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
            enabled.Toggled += (_, _) => rule.Enabled = enabled.IsOn;
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
            up.Click += (_, _) => { (_routingRules[i - 1], _routingRules[i]) = (_routingRules[i], _routingRules[i - 1]); RenderRoutingRules(); };
            down.Click += (_, _) => { (_routingRules[i + 1], _routingRules[i]) = (_routingRules[i], _routingRules[i + 1]); RenderRoutingRules(); };
            edit.Click += async (_, _) => { if (await EditRuleAsync(rule, false)) RenderRoutingRules(); };
            del.Click += async (_, _) =>
            {
                var dialog = new ContentDialog { XamlRoot = Content.XamlRoot, Title = "删除路由规则", Content = $"确定删除“{rule.Name}”吗？", PrimaryButtonText = "删除", CloseButtonText = "取消", DefaultButton = ContentDialogButton.Close };
                if (await dialog.ShowAsync() == ContentDialogResult.Primary) { _routingRules.Remove(rule); RenderRoutingRules(); }
            };
            ops.Children.Add(up); ops.Children.Add(down); ops.Children.Add(edit); ops.Children.Add(del);
            Grid.SetColumn(ops, 2); grid.Children.Add(ops);
            RoutingRulesPanel.Children.Add(Card(grid));
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
        var exit = new TextBox { Header = "指定出口 Device ID（可选）", Text = rule.ExitId };
        var datagram = new ToggleSwitch { Header = "必须使用原生数据报", IsOn = rule.DatagramRequired };
        var handleDirect = new ToggleSwitch { Header = "由 RelayProxy 处理 DIRECT", IsOn = rule.HandleDirect };
        var panel = new StackPanel { Spacing = 10 };
        foreach (var control in new UIElement[] { name, processes, targets, ports, protocols, action, exit, datagram, handleDirect }) panel.Children.Add(control);
        var dialog = new ContentDialog { XamlRoot = Content.XamlRoot, Title = isNew ? "添加路由规则" : "编辑路由规则", Content = new ScrollViewer { Content = panel, MaxHeight = 560 }, PrimaryButtonText = "确定", CloseButtonText = "取消", DefaultButton = ContentDialogButton.Primary };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return false;
        if (string.IsNullOrWhiteSpace(name.Text)) { ShowInfo(RoutingBar, "规则名称不能为空", "", InfoBarSeverity.Warning); return false; }
        rule.Name = name.Text.Trim(); rule.Processes = SplitList(processes.Text); rule.Targets = SplitList(targets.Text); rule.Ports = SplitList(ports.Text);
        rule.Protocols = SplitList(protocols.Text).Select(x => x.ToLowerInvariant()).ToList(); rule.Action = ComboTag(action, "PROXY"); rule.ExitId = exit.Text.Trim();
        rule.DatagramRequired = datagram.IsOn; rule.HandleDirect = handleDirect.IsOn;
        return true;
    }

    private async void ReloadConfig_Click(object sender, RoutedEventArgs e)
    {
        try { var result = await App.AgentApi.ReloadConfigAsync(); HandleSaveResult(ConnectionBar, result); await LoadConfigAsync(); }
        catch (Exception ex) { ShowInfo(ConnectionBar, "重新读取失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void RefreshNetworkService_Click(object sender, RoutedEventArgs e) => await RefreshNetworkServiceAsync();

    private async void RepairNetworkService_Click(object sender, RoutedEventArgs e)
    {
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
            TransparentProxySwitch.IsOn = false;
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
        try { await App.AgentApi.ClearLogsAsync(); LogsTextBox.Text = ""; ShowInfo(DiagnosticsBar, "日志已清空", "", InfoBarSeverity.Success); }
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
            var result = await App.AgentApi.SaveConfigAsync(new { revision = _config.Revision, gui = new { minimizeToTray = MinimizeToTraySwitch.IsOn, theme, verificationPopupTimeoutSec = SafeInt(PopupTimeoutBox, 15) } });
            ApplyTheme(theme);
            HandleSaveResult(SettingsBar, result); await LoadConfigAsync();
        }
        catch (Exception ex) { ShowInfo(SettingsBar, "保存失败", ex.Message, InfoBarSeverity.Error); }
    }

    private async void RestartAgent_Click(object sender, RoutedEventArgs e)
    {
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
        try { await App.AgentApi.ClearMessagesAsync(); MessagesPanel.Children.Clear(); ShowInfo(MessagesBar, "消息历史已清空", "", InfoBarSeverity.Success); }
        catch (Exception ex) { ShowInfo(MessagesBar, "清空失败", ex.Message, InfoBarSeverity.Error); }
    }
    private void OpenExits_Click(object sender, RoutedEventArgs e) => SelectNavigation("exits");

    private void SelectNavigation(string tag)
    {
        var item = Navigation.MenuItems.OfType<NavigationViewItem>().FirstOrDefault(x => (x.Tag as string) == tag);
        if (item is not null) Navigation.SelectedItem = item;
        ShowPage(tag);
    }

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
    private static string FormatCreatedAt(long value)
    {
        if (value <= 0) return "";
        try { return (value > 10_000_000_000 ? DateTimeOffset.FromUnixTimeMilliseconds(value) : DateTimeOffset.FromUnixTimeSeconds(value)).ToLocalTime().ToString("g"); }
        catch { return ""; }
    }
    private static string PopupTypeLabel(PushMessageDto m)
    {
        var type = !string.IsNullOrWhiteSpace(m.MessageType) ? m.MessageType : m.PopupType;
        if (string.IsNullOrWhiteSpace(type) && !string.IsNullOrWhiteSpace(m.VerificationCode)) type = "verification_code";
        return type switch { "verification_code" => "验证码", "important" => "重要提醒", _ => "普通消息" };
    }
}
