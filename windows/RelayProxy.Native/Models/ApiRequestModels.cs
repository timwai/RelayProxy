using System.Text.Json.Serialization;

namespace RelayProxy.Native.Models;

public sealed class EmptyRequestDto
{
    public static EmptyRequestDto Instance { get; } = new();
}

public sealed class SpeedTestRequestDto
{
    [JsonPropertyName("exitId")] public string ExitId { get; set; } = "";
    [JsonPropertyName("durationSeconds")] public int DurationSeconds { get; set; }
}

public sealed class SelectExitRequestDto
{
    [JsonPropertyName("exitId")] public string ExitId { get; set; } = "";
}

public sealed class ProxyPauseRequestDto
{
    [JsonPropertyName("paused")] public bool Paused { get; set; }
}

public sealed class RdpConnectRequestDto
{
    [JsonPropertyName("targetId")] public string TargetId { get; set; } = "";
    [JsonPropertyName("autoLaunch")] public bool AutoLaunch { get; set; } = true;
}

public sealed class AutostartRequestDto
{
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
}

public sealed class ServerConsoleLoginRequestDto
{
    [JsonPropertyName("username")] public string Username { get; set; } = "";
    [JsonPropertyName("password")] public string Password { get; set; } = "";
}

public sealed class MessageChannelUpdateRequestDto
{
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("allDevices")] public bool AllDevices { get; set; }
    [JsonPropertyName("deviceIds")] public List<string> DeviceIds { get; set; } = [];
    [JsonPropertyName("messageRules")] public List<MessageRuleDto> MessageRules { get; set; } = [];
}

public sealed class AgentConfigUpdateDto
{
    [JsonPropertyName("revision")] public string Revision { get; set; } = "";
    [JsonPropertyName("server"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public ServerUpdateDto? Server { get; set; }
    [JsonPropertyName("device"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public DeviceUpdateDto? Device { get; set; }
    [JsonPropertyName("transport"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public string? Transport { get; set; }
    [JsonPropertyName("p2p"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public P2PUpdateDto? P2P { get; set; }
    [JsonPropertyName("direct"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public DirectUpdateDto? Direct { get; set; }
    [JsonPropertyName("proxy"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public ProxyUpdateDto? Proxy { get; set; }
    [JsonPropertyName("network"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public NetworkUpdateDto? Network { get; set; }
    [JsonPropertyName("exit"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public ExitUpdateDto? Exit { get; set; }
    [JsonPropertyName("routing"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public RoutingUpdateDto? Routing { get; set; }
    [JsonPropertyName("gui"), JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)] public GuiUpdateDto? Gui { get; set; }
}

public sealed class ServerUpdateDto
{
    [JsonPropertyName("address")] public string Address { get; set; } = "";
    [JsonPropertyName("quicPort")] public int QuicPort { get; set; }
    [JsonPropertyName("tcpPort")] public int TcpPort { get; set; }
    [JsonPropertyName("tlsEnabled")] public bool TlsEnabled { get; set; }
}

public sealed class DeviceUpdateDto
{
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("identityId")] public string IdentityId { get; set; } = "";
}

public sealed class P2PUpdateDto
{
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("mode")] public string Mode { get; set; } = "auto";
    [JsonPropertyName("punchTimeoutMs")] public int PunchTimeoutMs { get; set; }
    [JsonPropertyName("keepaliveSec")] public int KeepaliveSec { get; set; }
    [JsonPropertyName("idleTimeoutSec")] public int IdleTimeoutSec { get; set; }
    [JsonPropertyName("maxExitSessions")] public int MaxExitSessions { get; set; }
    [JsonPropertyName("fallback")] public bool Fallback { get; set; }
}

public sealed class DirectUpdateDto
{
    [JsonPropertyName("public")] public PublicDirectUpdateDto Public { get; set; } = new();
}

public sealed class PublicDirectUpdateDto
{
    [JsonPropertyName("advertise")] public string Advertise { get; set; } = "";
}

public sealed class ProxyUpdateDto
{
    [JsonPropertyName("socks5Enabled")] public bool Socks5Enabled { get; set; }
    [JsonPropertyName("socks5Listen")] public string Socks5Listen { get; set; } = "";
    [JsonPropertyName("socks5Port")] public int Socks5Port { get; set; }
    [JsonPropertyName("httpEnabled")] public bool HttpEnabled { get; set; }
    [JsonPropertyName("httpListen")] public string HttpListen { get; set; } = "";
    [JsonPropertyName("httpPort")] public int HttpPort { get; set; }
}

public sealed class NetworkUpdateDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "";
    [JsonPropertyName("excludeProcesses")] public List<string> ExcludeProcesses { get; set; } = [];
}

public sealed class ExitUpdateDto
{
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("allowInternet")] public bool AllowInternet { get; set; }
    [JsonPropertyName("allowPrivateNetwork")] public bool AllowPrivateNetwork { get; set; }
    [JsonPropertyName("allowLoopback")] public bool AllowLoopback { get; set; }
    [JsonPropertyName("upstream")] public ExitUpstreamUpdateDto Upstream { get; set; } = new();
    [JsonPropertyName("access")] public ExitAccessUpdateDto Access { get; set; } = new();
}

public sealed class ExitUpstreamUpdateDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "";
    [JsonPropertyName("address")] public string Address { get; set; } = "";
    [JsonPropertyName("username")] public string Username { get; set; } = "";
    [JsonPropertyName("password")] public string Password { get; set; } = "";
}

public sealed class ExitAccessUpdateDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "";
    [JsonPropertyName("domains")] public List<string> Domains { get; set; } = [];
    [JsonPropertyName("cidrs")] public List<string> Cidrs { get; set; } = [];
}

public sealed class RoutingUpdateDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "rule";
    [JsonPropertyName("default_action")] public string DefaultAction { get; set; } = "PROXY";
    [JsonPropertyName("rules")] public List<RoutingRuleDto> Rules { get; set; } = [];
}

public sealed class GuiUpdateDto
{
    [JsonPropertyName("minimizeToTray")] public bool MinimizeToTray { get; set; }
    [JsonPropertyName("systemNotifications")] public bool SystemNotifications { get; set; }
    [JsonPropertyName("theme")] public string Theme { get; set; } = "system";
    [JsonPropertyName("verificationPopupTimeoutSec")] public int VerificationPopupTimeoutSec { get; set; }
}

public sealed class DiagnosticsManifestDto
{
    [JsonPropertyName("format")] public string Format { get; set; } = "relayproxy-diagnostic-v1";
    [JsonPropertyName("collectedAt")] public DateTimeOffset CollectedAt { get; set; }
    [JsonPropertyName("durationSeconds")] public int DurationSeconds { get; set; }
    [JsonPropertyName("sampleIntervalSeconds")] public int SampleIntervalSeconds { get; set; }
    [JsonPropertyName("sampleCount")] public int SampleCount { get; set; }
    [JsonPropertyName("note")] public string Note { get; set; } = "";
}
