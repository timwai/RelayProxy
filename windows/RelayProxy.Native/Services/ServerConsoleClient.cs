using System.Net;
using System.Net.Http.Json;
using System.Text.Json;
using RelayProxy.Native.Models;

namespace RelayProxy.Native.Services;

public sealed class ServerConsoleClient : IDisposable
{
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    {
        PropertyNameCaseInsensitive = true
    };

    private CookieContainer _cookies = new();
    private HttpClient? _http;
    private bool _disposed;

    public Uri? BaseAddress { get; private set; }
    public ServerConsoleUserDto? CurrentUser { get; private set; }
    public bool IsAuthenticated => _http is not null && CurrentUser is not null;

    public async Task<ServerConsoleUserDto> LoginAsync(string baseAddress, string username, string password, CancellationToken ct = default)
    {
        ThrowIfDisposed();
        var uri = NormalizeBaseAddress(baseAddress);
        username = username.Trim();
        if (username.Length == 0) throw new InvalidOperationException("请输入 Server Console 用户名。");
        if (password.Length == 0) throw new InvalidOperationException("请输入 Server Console 密码。");

        ResetClient();
        _cookies = new CookieContainer();
        var handler = new HttpClientHandler
        {
            CookieContainer = _cookies,
            UseCookies = true,
            AllowAutoRedirect = false,
            AutomaticDecompression = DecompressionMethods.GZip | DecompressionMethods.Deflate
        };
        _http = new HttpClient(handler, disposeHandler: true)
        {
            BaseAddress = uri,
            Timeout = TimeSpan.FromSeconds(15)
        };

        try
        {
            using var response = await _http.PostAsJsonAsync("api/v1/auth/login", new { username, password }, Json, ct);
            await EnsureSuccessAsync(response, ct);
            var body = await response.Content.ReadFromJsonAsync<ServerConsoleLoginResponseDto>(Json, ct)
                ?? throw new InvalidOperationException("Server Console 登录响应为空。");
            CurrentUser = body.User;
            BaseAddress = uri;
            return body.User;
        }
        catch
        {
            ResetClient();
            throw;
        }
    }

    public async Task<IReadOnlyList<MessageChannelDto>> GetMessageChannelsAsync(CancellationToken ct = default)
    {
        var http = EnsureAuthenticated();
        using var response = await http.GetAsync("api/v1/message-channels", ct);
        await EnsureSuccessAsync(response, ct);
        return await response.Content.ReadFromJsonAsync<List<MessageChannelDto>>(Json, ct) ?? [];
    }

    public async Task<MessageChannelDto> UpdateMessageRulesAsync(MessageChannelDto channel, IReadOnlyList<MessageRuleDto> rules, CancellationToken ct = default)
    {
        var http = EnsureAuthenticated();
        if (string.IsNullOrWhiteSpace(channel.Id)) throw new InvalidOperationException("消息渠道 ID 为空。");

        var body = new
        {
            name = channel.Name,
            allDevices = channel.AllDevices,
            deviceIds = channel.DeviceIds,
            messageRules = rules
        };
        using var response = await http.PutAsJsonAsync($"api/v1/message-channels/{Uri.EscapeDataString(channel.Id)}", body, Json, ct);
        await EnsureSuccessAsync(response, ct);
        return await response.Content.ReadFromJsonAsync<MessageChannelDto>(Json, ct)
            ?? throw new InvalidOperationException("Server Console 未返回更新后的消息渠道。");
    }

    public async Task LogoutAsync(CancellationToken ct = default)
    {
        if (_http is not null)
        {
            try
            {
                using var response = await _http.PostAsync("api/v1/auth/logout", content: null, ct);
            }
            catch { }
        }
        ResetClient();
    }

    private HttpClient EnsureAuthenticated()
    {
        ThrowIfDisposed();
        if (_http is null || CurrentUser is null)
            throw new InvalidOperationException("请先登录 Server Console。");
        return _http;
    }

    private static Uri NormalizeBaseAddress(string value)
    {
        value = value.Trim();
        if (!Uri.TryCreate(value, UriKind.Absolute, out var uri) ||
            (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps) ||
            string.IsNullOrWhiteSpace(uri.Host))
            throw new InvalidOperationException("Server Console 地址必须是完整的 http:// 或 https:// 地址，例如 http://relay.example.com:20001。");

        var builder = new UriBuilder(uri) { Path = "/", Query = "", Fragment = "" };
        return builder.Uri;
    }

    private static async Task EnsureSuccessAsync(HttpResponseMessage response, CancellationToken ct)
    {
        if (response.IsSuccessStatusCode) return;
        string message;
        try
        {
            using var doc = JsonDocument.Parse(await response.Content.ReadAsStringAsync(ct));
            message = doc.RootElement.TryGetProperty("error", out var error)
                ? error.GetString() ?? response.ReasonPhrase ?? "Server Console 请求失败"
                : response.ReasonPhrase ?? "Server Console 请求失败";
        }
        catch
        {
            message = response.ReasonPhrase ?? "Server Console 请求失败";
        }
        throw new InvalidOperationException($"{(int)response.StatusCode} {message}");
    }

    private void ResetClient()
    {
        CurrentUser = null;
        BaseAddress = null;
        _http?.Dispose();
        _http = null;
        _cookies = new CookieContainer();
    }

    private void ThrowIfDisposed()
    {
        ObjectDisposedException.ThrowIf(_disposed, this);
    }

    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        ResetClient();
    }
}
