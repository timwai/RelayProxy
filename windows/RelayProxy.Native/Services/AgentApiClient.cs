using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using RelayProxy.Native.Models;

namespace RelayProxy.Native.Services;

public sealed class AgentApiClient : IDisposable
{
    private HttpClient _http = new();
    public Uri? BaseAddress { get; private set; }
    public bool IsReady => BaseAddress is not null;
    public bool IsPrivateNativeChannel { get; private set; }

    public void Configure(Uri managementUrl, string? explicitToken = null)
    {
        IsPrivateNativeChannel = !string.IsNullOrWhiteSpace(explicitToken);
        var token = IsPrivateNativeChannel ? explicitToken : GetQueryValue(managementUrl, "token");
        var builder = new UriBuilder(managementUrl) { Query = "", Fragment = "", Path = "/" };
        BaseAddress = builder.Uri;

        var previous = _http;
        _http = new HttpClient
        {
            BaseAddress = BaseAddress,
            Timeout = TimeSpan.FromSeconds(15),
        };
        if (!string.IsNullOrWhiteSpace(token))
            _http.DefaultRequestHeaders.Authorization = new AuthenticationHeaderValue("Bearer", token);
        previous.Dispose();
    }

    public void Reset()
    {
        BaseAddress = null;
        IsPrivateNativeChannel = false;
        var previous = _http;
        _http = new HttpClient { Timeout = TimeSpan.FromSeconds(15) };
        previous.Dispose();
    }

    public Task<AgentStatusDto?> GetStatusAsync(CancellationToken ct = default) => GetAsync("api/status", RelayProxyJsonContext.Get<AgentStatusDto>(), ct);
    public Task<AgentConfigDto?> GetConfigAsync(CancellationToken ct = default) => GetAsync("api/config", RelayProxyJsonContext.Get<AgentConfigDto>(), ct);
    public async Task<List<ProxyExitDto>> GetExitsAsync(CancellationToken ct = default) => await GetAsync("api/proxy/exits", RelayProxyJsonContext.Get<List<ProxyExitDto>>(), ct) ?? [];
    public Task<TrafficSnapshotDto?> GetConnectionsAsync(CancellationToken ct = default) => GetAsync("api/connections", RelayProxyJsonContext.Get<TrafficSnapshotDto>(), ct);
    public async Task<List<PushMessageDto>> GetMessagesAsync(CancellationToken ct = default) => await GetAsync("api/messages", RelayProxyJsonContext.Get<List<PushMessageDto>>(), ct) ?? [];
    public async Task<List<RdpTargetDto>> GetRdpTargetsAsync(CancellationToken ct = default) => await GetAsync("api/rdp/targets", RelayProxyJsonContext.Get<List<RdpTargetDto>>(), ct) ?? [];
    public async Task<List<LogEntryDto>> GetLogsAsync(CancellationToken ct = default) => await GetAsync("api/logs", RelayProxyJsonContext.Get<List<LogEntryDto>>(), ct) ?? [];
    public Task<DiagnosticsSnapshotDto?> GetDiagnosticsAsync(CancellationToken ct = default) => GetAsync("api/diagnostics", RelayProxyJsonContext.Get<DiagnosticsSnapshotDto>(), ct);
    public Task<NetworkServiceStatusDto?> GetNetworkServiceStatusAsync(CancellationToken ct = default) => GetAsync("api/network-service", RelayProxyJsonContext.Get<NetworkServiceStatusDto>(), ct);

    public async Task<SaveResultDto?> SaveConfigAsync(AgentConfigUpdateDto update, CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PutAsJsonAsync("api/config", update, RelayProxyJsonContext.Get<AgentConfigUpdateDto>(), ct);
        return await ReadMutationAsync(response, RelayProxyJsonContext.Get<SaveResultDto>(), ct);
    }

    public async Task<SaveResultDto?> ReloadConfigAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/reload", EmptyRequestDto.Instance, RelayProxyJsonContext.Get<EmptyRequestDto>(), ct);
        return await ReadMutationAsync(response, RelayProxyJsonContext.Get<SaveResultDto>(), ct);
    }

    public async Task<SpeedTestResultDto?> RunSpeedTestAsync(string exitId, int durationSeconds = 2, CancellationToken ct = default)
    {
        EnsureReady();
        durationSeconds = Math.Clamp(durationSeconds, 1, 10);
        var request = new SpeedTestRequestDto { ExitId = exitId, DurationSeconds = durationSeconds };
        using var response = await _http.PostAsJsonAsync("api/speed-test", request, RelayProxyJsonContext.Get<SpeedTestRequestDto>(), ct);
        var payload = await ReadMutationAsync(response, RelayProxyJsonContext.Get<SpeedTestApiResponseDto>(), ct);
        return payload?.Result;
    }

    public async Task SelectExitAsync(string exitId, CancellationToken ct = default)
    {
        EnsureReady();
        var request = new SelectExitRequestDto { ExitId = exitId };
        using var response = await _http.PostAsJsonAsync("api/select-exit", request, RelayProxyJsonContext.Get<SelectExitRequestDto>(), ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task<AgentStatusDto?> SetProxyPausedAsync(bool paused, CancellationToken ct = default)
    {
        EnsureReady();
        var request = new ProxyPauseRequestDto { Paused = paused };
        using var response = await _http.PostAsJsonAsync("api/proxy/pause", request, RelayProxyJsonContext.Get<ProxyPauseRequestDto>(), ct);
        return await ReadMutationAsync(response, RelayProxyJsonContext.Get<AgentStatusDto>(), ct);
    }

    public async Task<RdpConnectResponseDto?> ConnectRdpAsync(string targetId, bool autoLaunch = true, CancellationToken ct = default)
    {
        EnsureReady();
        var request = new RdpConnectRequestDto { TargetId = targetId, AutoLaunch = autoLaunch };
        using var response = await _http.PostAsJsonAsync("api/rdp/connect", request, RelayProxyJsonContext.Get<RdpConnectRequestDto>(), ct);
        return await ReadMutationAsync(response, RelayProxyJsonContext.Get<RdpConnectResponseDto>(), ct);
    }

    public async Task DisconnectRdpAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/rdp/disconnect", EmptyRequestDto.Instance, RelayProxyJsonContext.Get<EmptyRequestDto>(), ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task<MutationMessageDto?> RepairNetworkServiceAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/network-service/repair", EmptyRequestDto.Instance, RelayProxyJsonContext.Get<EmptyRequestDto>(), ct);
        return await ReadMutationAsync(response, RelayProxyJsonContext.Get<MutationMessageDto>(), ct);
    }

    public async Task<MutationMessageDto?> UninstallNetworkServiceAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.DeleteAsync("api/network-service", ct);
        return await ReadMutationAsync(response, RelayProxyJsonContext.Get<MutationMessageDto>(), ct);
    }

    public async Task SetAutostartAsync(bool enabled, CancellationToken ct = default)
    {
        EnsureReady();
        var request = new AutostartRequestDto { Enabled = enabled };
        using var response = await _http.PostAsJsonAsync("api/autostart", request, RelayProxyJsonContext.Get<AutostartRequestDto>(), ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task ClearMessagesAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.DeleteAsync("api/messages", ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task ClearLogsAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.DeleteAsync("api/logs", ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task ClearConnectionsAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.DeleteAsync("api/connections", ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task QuitAsync(CancellationToken ct = default)
    {
        if (!IsReady) return;
        try
        {
            using var _ = await _http.PostAsJsonAsync("api/quit", EmptyRequestDto.Instance, RelayProxyJsonContext.Get<EmptyRequestDto>(), ct);
        }
        catch { }
    }

    private async Task<T?> GetAsync<T>(string path, JsonTypeInfo<T> typeInfo, CancellationToken ct)
    {
        EnsureReady();
        using var response = await _http.GetAsync(path, ct);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(ct);
        return await JsonSerializer.DeserializeAsync(stream, typeInfo, ct);
    }

    private static async Task<T?> ReadMutationAsync<T>(HttpResponseMessage response, JsonTypeInfo<T> typeInfo, CancellationToken ct)
    {
        var body = await response.Content.ReadAsStringAsync(ct);
        if (!response.IsSuccessStatusCode) throw new InvalidOperationException(ReadError(body, response.StatusCode.ToString()));
        return JsonSerializer.Deserialize(body, typeInfo);
    }

    private static async Task EnsureMutationAsync(HttpResponseMessage response, CancellationToken ct)
    {
        if (response.IsSuccessStatusCode) return;
        var body = await response.Content.ReadAsStringAsync(ct);
        throw new InvalidOperationException(ReadError(body, response.StatusCode.ToString()));
    }

    private static string ReadError(string body, string fallback)
    {
        try
        {
            using var doc = JsonDocument.Parse(body);
            if (doc.RootElement.TryGetProperty("message", out var message)) return message.GetString() ?? fallback;
        }
        catch { }
        return string.IsNullOrWhiteSpace(body) ? fallback : body.Trim();
    }

    private void EnsureReady()
    {
        if (!IsReady) throw new InvalidOperationException("RelayProxy Agent management API is not ready yet.");
    }

    private static string? GetQueryValue(Uri uri, string name)
    {
        foreach (var pair in uri.Query.TrimStart('?').Split('&', StringSplitOptions.RemoveEmptyEntries))
        {
            var parts = pair.Split('=', 2);
            if (string.Equals(Uri.UnescapeDataString(parts[0]), name, StringComparison.OrdinalIgnoreCase))
                return parts.Length == 2 ? Uri.UnescapeDataString(parts[1]) : "";
        }
        return null;
    }

    public void Dispose() => _http.Dispose();
}
