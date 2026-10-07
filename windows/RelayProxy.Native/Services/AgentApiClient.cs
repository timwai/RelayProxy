using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json;
using RelayProxy.Native.Models;

namespace RelayProxy.Native.Services;

public sealed class AgentApiClient : IDisposable
{
    private readonly HttpClient _http = new();
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web) { PropertyNameCaseInsensitive = true };
    public Uri? BaseAddress { get; private set; }
    public bool IsReady => BaseAddress is not null;

    public void Configure(Uri managementUrl)
    {
        var token = GetQueryValue(managementUrl, "token");
        var builder = new UriBuilder(managementUrl) { Query = "", Fragment = "", Path = "/" };
        BaseAddress = builder.Uri;
        _http.BaseAddress = BaseAddress;
        _http.Timeout = TimeSpan.FromSeconds(15);
        _http.DefaultRequestHeaders.Authorization = string.IsNullOrWhiteSpace(token) ? null : new AuthenticationHeaderValue("Bearer", token);
    }

    public Task<AgentStatusDto?> GetStatusAsync(CancellationToken ct = default) => GetAsync<AgentStatusDto>("api/status", ct);
    public Task<AgentConfigDto?> GetConfigAsync(CancellationToken ct = default) => GetAsync<AgentConfigDto>("api/config", ct);
    public async Task<List<ProxyExitDto>> GetExitsAsync(CancellationToken ct = default) => await GetAsync<List<ProxyExitDto>>("api/proxy/exits", ct) ?? [];
    public Task<TrafficSnapshotDto?> GetConnectionsAsync(CancellationToken ct = default) => GetAsync<TrafficSnapshotDto>("api/connections", ct);
    public async Task<List<PushMessageDto>> GetMessagesAsync(CancellationToken ct = default) => await GetAsync<List<PushMessageDto>>("api/messages", ct) ?? [];
    public async Task<List<RdpTargetDto>> GetRdpTargetsAsync(CancellationToken ct = default) => await GetAsync<List<RdpTargetDto>>("api/rdp/targets", ct) ?? [];
    public async Task<List<LogEntryDto>> GetLogsAsync(CancellationToken ct = default) => await GetAsync<List<LogEntryDto>>("api/logs", ct) ?? [];
    public Task<DiagnosticsSnapshotDto?> GetDiagnosticsAsync(CancellationToken ct = default) => GetAsync<DiagnosticsSnapshotDto>("api/diagnostics", ct);
    public Task<NetworkServiceStatusDto?> GetNetworkServiceStatusAsync(CancellationToken ct = default) => GetAsync<NetworkServiceStatusDto>("api/network-service", ct);

    public async Task<SaveResultDto?> SaveConfigAsync(object update, CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PutAsJsonAsync("api/config", update, Json, ct);
        return await ReadMutationAsync<SaveResultDto>(response, ct);
    }

    public async Task<SaveResultDto?> ReloadConfigAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/reload", new { }, Json, ct);
        return await ReadMutationAsync<SaveResultDto>(response, ct);
    }

    public async Task SelectExitAsync(string exitId, CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/select-exit", new { exitId }, Json, ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task<RdpConnectResponseDto?> ConnectRdpAsync(string targetId, bool autoLaunch = true, CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/rdp/connect", new { targetId, autoLaunch }, Json, ct);
        return await ReadMutationAsync<RdpConnectResponseDto>(response, ct);
    }

    public async Task DisconnectRdpAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/rdp/disconnect", new { }, Json, ct);
        await EnsureMutationAsync(response, ct);
    }

    public async Task<MutationMessageDto?> RepairNetworkServiceAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/network-service/repair", new { }, Json, ct);
        return await ReadMutationAsync<MutationMessageDto>(response, ct);
    }

    public async Task<MutationMessageDto?> UninstallNetworkServiceAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.DeleteAsync("api/network-service", ct);
        return await ReadMutationAsync<MutationMessageDto>(response, ct);
    }

    public async Task SetAutostartAsync(bool enabled, CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/autostart", new { enabled }, Json, ct);
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
        try { using var _ = await _http.PostAsJsonAsync("api/quit", new { }, Json, ct); } catch { }
    }

    private async Task<T?> GetAsync<T>(string path, CancellationToken ct)
    {
        EnsureReady();
        using var response = await _http.GetAsync(path, ct);
        response.EnsureSuccessStatusCode();
        await using var stream = await response.Content.ReadAsStreamAsync(ct);
        return await JsonSerializer.DeserializeAsync<T>(stream, Json, ct);
    }

    private static async Task<T?> ReadMutationAsync<T>(HttpResponseMessage response, CancellationToken ct)
    {
        var body = await response.Content.ReadAsStringAsync(ct);
        if (!response.IsSuccessStatusCode) throw new InvalidOperationException(ReadError(body, response.StatusCode.ToString()));
        return JsonSerializer.Deserialize<T>(body, Json);
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
