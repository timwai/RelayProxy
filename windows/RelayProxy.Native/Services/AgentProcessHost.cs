using System.Diagnostics;
using System.Security.Cryptography;

namespace RelayProxy.Native.Services;

public sealed class AgentProcessHost
{
    private const string Marker = "[Web] Management page: ";
    private const string NativeTokenEnvironment = "RELAYPROXY_NATIVE_MANAGEMENT_TOKEN";
    private readonly AgentApiClient _api;
    private Process? _process;
    private bool _ownsProcess;
    private bool _stopping;
    private string? _configPath;

    public event Action<string>? StateChanged;
    public string State { get; private set; } = "未启动";
    public bool CanRestart => _ownsProcess && _process is not null;

    public AgentProcessHost(AgentApiClient api) => _api = api;

    public async Task StartAsync(string? configPath = null, CancellationToken ct = default)
    {
        if (!string.IsNullOrWhiteSpace(configPath)) _configPath = Path.GetFullPath(configPath);
        var existing = Environment.GetEnvironmentVariable("RELAYPROXY_MANAGEMENT_URL");
        if (Uri.TryCreate(existing, UriKind.Absolute, out var existingUri))
        {
            _ownsProcess = false;
            _api.Configure(existingUri);
            SetState("已连接现有 Agent");
            return;
        }

        var exe = ResolveAgentPath();
        if (exe is null)
        {
            SetState("未找到 relay-agent.exe");
            return;
        }

        var managementToken = Convert.ToHexString(RandomNumberGenerator.GetBytes(32));
        var ready = new TaskCompletionSource<Uri>(TaskCreationOptions.RunContinuationsAsynchronously);
        var startInfo = new ProcessStartInfo
        {
            FileName = exe,
            WorkingDirectory = Path.GetDirectoryName(exe) ?? AppContext.BaseDirectory,
            UseShellExecute = false,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            CreateNoWindow = true,
        };
        startInfo.ArgumentList.Add("--no-gui");
        if (!string.IsNullOrWhiteSpace(_configPath))
        {
            startInfo.ArgumentList.Add("--config");
            startInfo.ArgumentList.Add(_configPath);
        }
        startInfo.Environment[NativeTokenEnvironment] = managementToken;
        var publishedGui = Path.Combine(AppContext.BaseDirectory, "relay-agent-gui.exe");
        var nativeGuiPath = File.Exists(publishedGui) ? publishedGui : Environment.ProcessPath;
        if (!string.IsNullOrWhiteSpace(nativeGuiPath))
            startInfo.Environment["RELAYPROXY_NATIVE_GUI_PATH"] = nativeGuiPath;

        var process = new Process { StartInfo = startInfo, EnableRaisingEvents = true };
        _process = process;
        _stopping = false;
        process.Exited += (_, _) =>
        {
            _api.Reset();
            _ownsProcess = false;
            if (!_stopping) SetState("Agent 意外退出");
        };

        try
        {
            if (!process.Start())
            {
                SetState("Agent 启动失败");
                return;
            }
            _ownsProcess = true;
            SetState("正在连接 Agent…");
            _ = PumpAsync(process.StandardOutput, ready, ct);
            _ = PumpAsync(process.StandardError, ready, ct);
            var management = await ready.Task.WaitAsync(TimeSpan.FromSeconds(15), ct);
            _api.Configure(management, managementToken);
            SetState("Agent 正常运行");
        }
        catch (TimeoutException)
        {
            SetState(process.HasExited ? "Agent 已退出，本地管理接口未启动" : "本地管理接口未就绪");
        }
        catch (Exception ex)
        {
            SetState($"Agent 启动失败: {ex.Message}");
        }
    }

    public async Task StopAsync()
    {
        var process = _process;
        if (!_ownsProcess || process is null) return;

        _stopping = true;
        try
        {
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(2));
            await _api.QuitAsync(cts.Token);
        }
        catch { }

        try
        {
            if (!process.HasExited && !process.WaitForExit(2500))
                process.Kill(entireProcessTree: true);
        }
        catch { }

        _ownsProcess = false;
        _api.Reset();
        try { process.Dispose(); } catch { }
        if (ReferenceEquals(_process, process)) _process = null;
        SetState("Agent 已停止");
    }

    public async Task RestartAsync(CancellationToken ct = default)
    {
        if (!CanRestart)
            throw new InvalidOperationException("当前 GUI 连接的是外部 Agent，无法由此窗口自动重启。");

        SetState("正在重启 Agent…");
        await StopAsync();
        await StartAsync(_configPath, ct);
        if (!_api.IsReady)
            throw new InvalidOperationException("Agent 重启后本地管理接口未就绪。");
    }

    private async Task PumpAsync(StreamReader reader, TaskCompletionSource<Uri> ready, CancellationToken ct)
    {
        while (!ct.IsCancellationRequested)
        {
            var line = await reader.ReadLineAsync(ct);
            if (line is null) break;
            var i = line.IndexOf(Marker, StringComparison.Ordinal);
            if (i < 0) continue;
            if (Uri.TryCreate(line[(i + Marker.Length)..].Trim(), UriKind.Absolute, out var uri))
                ready.TrySetResult(uri);
        }
    }

    private static string? ResolveAgentPath()
    {
        var configured = Environment.GetEnvironmentVariable("RELAYPROXY_AGENT_PATH");
        if (!string.IsNullOrWhiteSpace(configured) && File.Exists(configured)) return configured;
        return new[]
        {
            Path.Combine(AppContext.BaseDirectory, "relay-agent.exe"),
            Path.Combine(AppContext.BaseDirectory, "agent", "relay-agent.exe"),
            Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "..", "relay-agent.exe")),
        }.FirstOrDefault(File.Exists);
    }

    private void SetState(string state)
    {
        State = state;
        StateChanged?.Invoke(state);
    }
}
