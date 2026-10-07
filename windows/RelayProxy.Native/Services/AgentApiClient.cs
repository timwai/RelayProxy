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
        _http.Timeout = TimeSpan.FromSeconds(10);
        _http.DefaultRequestHeaders.Authorization = string.IsNullOrWhiteSpace(token) ? null : new AuthenticationHeaderValue("Bearer", token);
    }

    public Task<AgentStatusDto?> GetStatusAsync(CancellationToken ct = default) => GetAsync<AgentStatusDto>("api/status", ct);
    public async Task<List<ProxyExitDto>> GetExitsAsync(CancellationToken ct = default) => await GetAsync<List<ProxyExitDto>>("api/proxy/exits", ct) ?? [];
    public Task<TrafficSnapshotDto?> GetConnectionsAsync(CancellationToken ct = default) => GetAsync<TrafficSnapshotDto>("api/connections", ct);
    public async Task<List<PushMessageDto>> GetMessagesAsync(CancellationToken ct = default) => await GetAsync<List<PushMessageDto>>("api/messages", ct) ?? [];

    public async Task SelectExitAsync(string exitId, CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.PostAsJsonAsync("api/select-exit", new { exitId }, Json, ct);
        response.EnsureSuccessStatusCode();
    }

    public async Task ClearMessagesAsync(CancellationToken ct = default)
    {
        EnsureReady();
        using var response = await _http.DeleteAsync("api/messages", ct);
        response.EnsureSuccessStatusCode();
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
