using System.Text.Json.Serialization;

namespace RelayProxy.Native.Models;

public sealed class AgentConfigDto
{
    [JsonPropertyName("configPath")] public string ConfigPath { get; set; } = "";
    [JsonPropertyName("serverAddress")] public string ServerAddress { get; set; } = "";
    [JsonPropertyName("quicPort")] public int QuicPort { get; set; }
    [JsonPropertyName("tcpPort")] public int TcpPort { get; set; }
    [JsonPropertyName("tlsEnabled")] public bool TlsEnabled { get; set; }
    [JsonPropertyName("deviceName")] public string DeviceName { get; set; } = "";
    [JsonPropertyName("identityId")] public string IdentityId { get; set; } = "";
    [JsonPropertyName("transport")] public string Transport { get; set; } = "auto";
    [JsonPropertyName("socks5")] public ProxyLegDto Socks5 { get; set; } = new();
    [JsonPropertyName("http")] public ProxyLegDto Http { get; set; } = new();
    [JsonPropertyName("defaultExitId")] public string DefaultExitId { get; set; } = "";
    [JsonPropertyName("exitEnabled")] public bool ExitEnabled { get; set; }
    [JsonPropertyName("allowInternet")] public bool AllowInternet { get; set; }
    [JsonPropertyName("allowPrivateNetwork")] public bool AllowPrivateNetwork { get; set; }
    [JsonPropertyName("allowLoopback")] public bool AllowLoopback { get; set; }
    [JsonPropertyName("accessMode")] public string AccessMode { get; set; } = "";
    [JsonPropertyName("accessDomains")] public List<string> AccessDomains { get; set; } = [];
    [JsonPropertyName("accessCidrs")] public List<string> AccessCidrs { get; set; } = [];
    [JsonPropertyName("exitUpstream")] public ExitUpstreamDto ExitUpstream { get; set; } = new();
    [JsonPropertyName("p2p")] public P2PConfigDto P2P { get; set; } = new();
    [JsonPropertyName("direct")] public DirectConfigDto Direct { get; set; } = new();
    [JsonPropertyName("networkMode")] public string NetworkMode { get; set; } = "";
    [JsonPropertyName("network")] public NetworkConfigDto Network { get; set; } = new();
    [JsonPropertyName("routing")] public RoutingConfigDto Routing { get; set; } = new();
    [JsonPropertyName("isAutostart")] public bool IsAutostart { get; set; }
    [JsonPropertyName("minimizeToTray")] public bool MinimizeToTray { get; set; }
    [JsonPropertyName("startMinimized")] public bool StartMinimized { get; set; }
    [JsonPropertyName("theme")] public string Theme { get; set; } = "system";
    [JsonPropertyName("verificationPopupTimeoutSec")] public int VerificationPopupTimeoutSec { get; set; } = 15;
    [JsonPropertyName("revision")] public string Revision { get; set; } = "";
    [JsonPropertyName("restartRequired")] public bool RestartRequired { get; set; }
    [JsonPropertyName("restartFields")] public List<string> RestartFields { get; set; } = [];
    [JsonPropertyName("reloadPending")] public bool ReloadPending { get; set; }
    [JsonPropertyName("configError")] public string ConfigError { get; set; } = "";
}
public sealed class ProxyLegDto
{
    [JsonPropertyName("enabled")] public bool Enabled { get; set; }
    [JsonPropertyName("listen")] public string Listen { get; set; } = "127.0.0.1";
    [JsonPropertyName("port")] public int Port { get; set; }
}
public sealed class ExitUpstreamDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "";
    [JsonPropertyName("address")] public string Address { get; set; } = "";
    [JsonPropertyName("username")] public string Username { get; set; } = "";
    [JsonPropertyName("password")] public string Password { get; set; } = "";
}
public sealed class P2PConfigDto
{
    [JsonPropertyName("enabled")] public bool Enabled { get; set; } = true;
    [JsonPropertyName("mode")] public string Mode { get; set; } = "auto";
    [JsonPropertyName("punchTimeoutMs")] public int PunchTimeoutMs { get; set; } = 3500;
    [JsonPropertyName("keepaliveSec")] public int KeepaliveSec { get; set; }
    [JsonPropertyName("idleTimeoutSec")] public int IdleTimeoutSec { get; set; } = 90;
    [JsonPropertyName("maxExitSessions")] public int MaxExitSessions { get; set; } = 4;
    [JsonPropertyName("fallback")] public bool Fallback { get; set; } = true;
}
public sealed class DirectConfigDto { [JsonPropertyName("publicAdvertise")] public string PublicAdvertise { get; set; } = ""; }
public sealed class NetworkConfigDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "";
    [JsonPropertyName("exclude_processes")] public List<string> ExcludeProcesses { get; set; } = [];
}
public sealed class RoutingConfigDto
{
    [JsonPropertyName("mode")] public string Mode { get; set; } = "global_proxy";
    [JsonPropertyName("default_action")] public string DefaultAction { get; set; } = "PROXY";
    [JsonPropertyName("rules")] public List<RoutingRuleDto> Rules { get; set; } = [];
}
public sealed class RoutingRuleDto
{
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("enabled")] public bool Enabled { get; set; } = true;
    [JsonPropertyName("action")] public string Action { get; set; } = "PROXY";
    [JsonPropertyName("exit_id")] public string ExitId { get; set; } = "";
    [JsonPropertyName("datagram_required")] public bool DatagramRequired { get; set; }
    [JsonPropertyName("handle_direct")] public bool HandleDirect { get; set; }
    [JsonPropertyName("processes")] public List<string> Processes { get; set; } = [];
    [JsonPropertyName("targets")] public List<string> Targets { get; set; } = [];
    [JsonPropertyName("ports")] public List<string> Ports { get; set; } = [];
    [JsonPropertyName("protocols")] public List<string> Protocols { get; set; } = [];
}
public sealed class SaveResultDto
{
    [JsonPropertyName("ok")] public bool Ok { get; set; }
    [JsonPropertyName("restartRequired")] public bool RestartRequired { get; set; }
    [JsonPropertyName("message")] public string Message { get; set; } = "";
    [JsonPropertyName("revision")] public string Revision { get; set; } = "";
    [JsonPropertyName("restartFields")] public List<string> RestartFields { get; set; } = [];
    [JsonPropertyName("reloadPending")] public bool ReloadPending { get; set; }
}
public sealed class RdpTargetDto
{
    [JsonPropertyName("deviceId")] public string DeviceId { get; set; } = "";
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("online")] public bool Online { get; set; }
}
public sealed class RdpConnectResponseDto
{
    [JsonPropertyName("ok")] public bool Ok { get; set; }
    [JsonPropertyName("target")] public RdpTargetDto? Target { get; set; }
}


public sealed class NetworkServiceStatusDto
{
    [JsonPropertyName("supported")] public bool Supported { get; set; }
    [JsonPropertyName("installed")] public bool Installed { get; set; }
    [JsonPropertyName("running")] public bool Running { get; set; }
    [JsonPropertyName("autoStart")] public bool AutoStart { get; set; }
    [JsonPropertyName("autoStartKnown")] public bool AutoStartKnown { get; set; }
    [JsonPropertyName("ready")] public bool Ready { get; set; }
    [JsonPropertyName("versionMatch")] public bool VersionMatch { get; set; }
    [JsonPropertyName("recoveryEnabled")] public bool RecoveryEnabled { get; set; }
    [JsonPropertyName("recoveryKnown")] public bool RecoveryKnown { get; set; }
    [JsonPropertyName("pid")] public uint Pid { get; set; }
    [JsonPropertyName("binaryPath")] public string BinaryPath { get; set; } = "";
    [JsonPropertyName("state")] public string State { get; set; } = "";
    [JsonPropertyName("message")] public string Message { get; set; } = "";
}

public sealed class MutationMessageDto
{
    [JsonPropertyName("ok")] public bool Ok { get; set; }
    [JsonPropertyName("message")] public string Message { get; set; } = "";
    [JsonPropertyName("rebootCleanup")] public bool RebootCleanup { get; set; }
    [JsonPropertyName("cleanupPath")] public string CleanupPath { get; set; } = "";
}
