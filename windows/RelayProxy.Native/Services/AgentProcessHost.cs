using System.Diagnostics;

namespace RelayProxy.Native.Services;

public sealed class AgentProcessHost
{
    private const string Marker = "[Web] Management page: ";
    private readonly AgentApiClient _api;
    private Process? _process;
    private bool _ownsProcess;
    public event Action<string>? StateChanged;
    public string State { get; private set; } = "未启动";

    public AgentProcessHost(AgentApiClient api) => _api = api;

    public async Task StartAsync(CancellationToken ct = default)
    {
        var existing = Environment.GetEnvironmentVariable("RELAYPROXY_MANAGEMENT_URL");
        if (Uri.TryCreate(existing, UriKind.Absolute, out var uri))
        {
            _api.Configure(uri); SetState("已连接现有 Agent"); return;
        }

        var exe = ResolveAgentPath();
        if (exe is null) { SetState("未找到 relay-agent.exe"); return; }
        var ready = new TaskCompletionSource<Uri>(TaskCreationOptions.RunContinuationsAsynchronously);
        _process = new Process
        {
            StartInfo = new ProcessStartInfo
            {
                FileName = exe, Arguments = "--no-gui", WorkingDirectory = Path.GetDirectoryName(exe) ?? AppContext.BaseDirectory,
                UseShellExecute = false, RedirectStandardOutput = true, RedirectStandardError = true, CreateNoWindow = true,
            },
            EnableRaisingEvents = true,
        };
        _process.Exited += (_, _) => { if (!_api.IsReady) SetState("Agent 已退出"); };

        try
        {
            if (!_process.Start()) { SetState("Agent 启动失败"); return; }
            _ownsProcess = true; SetState("正在连接 Agent…");
            _ = PumpAsync(_process.StandardOutput, ready, ct);
            _ = PumpAsync(_process.StandardError, ready, ct);
            var management = await ready.Task.WaitAsync(TimeSpan.FromSeconds(15), ct);
            _api.Configure(management); SetState("Agent 正常运行");
        }
        catch (TimeoutException) { SetState("本地管理接口未就绪"); }
        catch (Exception ex) { SetState($"Agent 启动失败: {ex.Message}"); }
    }

    public async Task StopAsync()
    {
        if (!_ownsProcess || _process is null) return;
        try { using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(2)); await _api.QuitAsync(cts.Token); } catch { }
        try { if (!_process.HasExited && !_process.WaitForExit(2500)) _process.Kill(entireProcessTree: true); } catch { }
    }

    private async Task PumpAsync(StreamReader reader, TaskCompletionSource<Uri> ready, CancellationToken ct)
    {
        while (!reader.EndOfStream && !ct.IsCancellationRequested)
        {
            var line = await reader.ReadLineAsync(ct); if (line is null) break;
            var i = line.IndexOf(Marker, StringComparison.Ordinal); if (i < 0) continue;
            if (Uri.TryCreate(line[(i + Marker.Length)..].Trim(), UriKind.Absolute, out var uri)) ready.TrySetResult(uri);
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

    private void SetState(string state) { State = state; StateChanged?.Invoke(state); }
}
