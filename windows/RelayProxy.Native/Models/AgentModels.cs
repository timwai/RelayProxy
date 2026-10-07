using System.Text.Json.Serialization;

namespace RelayProxy.Native.Models;

public sealed class AgentStatusDto
{
    [JsonPropertyName("connected")] public bool Connected { get; set; }
    [JsonPropertyName("transport")] public string Transport { get; set; } = "";
    [JsonPropertyName("latency")] public long LatencyMs { get; set; }
    [JsonPropertyName("deviceName")] public string DeviceName { get; set; } = "";
    [JsonPropertyName("identityName")] public string IdentityName { get; set; } = "";
    [JsonPropertyName("selectedExit")] public string SelectedExit { get; set; } = "";
    [JsonPropertyName("proxyExits")] public List<ProxyExitSummaryDto> ProxyExits { get; set; } = [];
    [JsonPropertyName("socks5Running")] public bool Socks5Running { get; set; }
    [JsonPropertyName("httpRunning")] public bool HttpRunning { get; set; }
    [JsonPropertyName("exitRunning")] public bool ExitRunning { get; set; }
    [JsonPropertyName("activeStreams")] public long ActiveStreams { get; set; }
    [JsonPropertyName("approvalState")] public string ApprovalState { get; set; } = "";
    [JsonPropertyName("rdpListenAddr")] public string RDPListenAddr { get; set; } = "";
    [JsonPropertyName("rdpTargetId")] public string RDPTargetID { get; set; } = "";
    [JsonPropertyName("rdpUdpEnabled")] public bool RDPUDPEnabled { get; set; }
    [JsonPropertyName("rdpUdpActive")] public bool RDPUDPActive { get; set; }
    [JsonPropertyName("rdpUdpReason")] public string RDPUDPReason { get; set; } = "";
    [JsonPropertyName("rdpPathTcp")] public string RDPPathTCP { get; set; } = "";
    [JsonPropertyName("rdpPathUdp")] public string RDPPathUDP { get; set; } = "";
    [JsonPropertyName("directState")] public string DirectState { get; set; } = "";
    [JsonPropertyName("directPath")] public string DirectPath { get; set; } = "";
    [JsonPropertyName("directRttMs")] public long DirectRttMs { get; set; }
    [JsonPropertyName("p2pState")] public string P2PState { get; set; } = "";
    [JsonPropertyName("p2pPath")] public string P2PPath { get; set; } = "";
    [JsonPropertyName("p2pRttMs")] public long P2PRttMs { get; set; }
}

public sealed class ProxyExitSummaryDto
{
    [JsonPropertyName("deviceId")] public string DeviceId { get; set; } = "";
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("authorizationSource")] public string AuthorizationSource { get; set; } = "";
    [JsonPropertyName("online")] public bool Online { get; set; }
}

public sealed class ProxyExitDto
{
    [JsonPropertyName("deviceId")] public string DeviceId { get; set; } = "";
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("identityName")] public string IdentityName { get; set; } = "";
    [JsonPropertyName("authorizationSource")] public string AuthorizationSource { get; set; } = "";
    [JsonPropertyName("online")] public bool Online { get; set; }
    [JsonPropertyName("direct")] public ProxyDirectPathsDto? Direct { get; set; }
}
public sealed class ProxyDirectPathsDto { [JsonPropertyName("public")] public ProxyPublicDirectPathDto? Public { get; set; } }
public sealed class ProxyPublicDirectPathDto
{
    [JsonPropertyName("available")] public bool Available { get; set; }
    [JsonPropertyName("transport")] public string Transport { get; set; } = "";
}

public sealed class TrafficSnapshotDto
{
    [JsonPropertyName("connections")] public List<ConnectionDto> Connections { get; set; } = [];
    [JsonPropertyName("active")] public int Active { get; set; }
    [JsonPropertyName("upload_rate")] public double UploadRate { get; set; }
    [JsonPropertyName("download_rate")] public double DownloadRate { get; set; }
}
public sealed class ConnectionDto
{
    [JsonPropertyName("id")] public ulong Id { get; set; }
    [JsonPropertyName("process_name")] public string ProcessName { get; set; } = "";
    [JsonPropertyName("process")] public string Process { get; set; } = "";
    [JsonPropertyName("host")] public string Host { get; set; } = "";
    [JsonPropertyName("ip")] public string Ip { get; set; } = "";
    [JsonPropertyName("port")] public int Port { get; set; }
    [JsonPropertyName("protocol")] public string Protocol { get; set; } = "";
    [JsonPropertyName("action")] public string Action { get; set; } = "";
    [JsonPropertyName("rule")] public string Rule { get; set; } = "";
    [JsonPropertyName("exit_id")] public string ExitId { get; set; } = "";
    [JsonPropertyName("state")] public string State { get; set; } = "";
    [JsonPropertyName("path")] public string Path { get; set; } = "";
    [JsonPropertyName("upload_rate")] public double UploadRate { get; set; }
    [JsonPropertyName("download_rate")] public double DownloadRate { get; set; }
}

public sealed class PushMessageDto
{
    [JsonPropertyName("id")] public string Id { get; set; } = "";
    [JsonPropertyName("title")] public string Title { get; set; } = "";
    [JsonPropertyName("content")] public string Content { get; set; } = "";
    [JsonPropertyName("messageType")] public string MessageType { get; set; } = "";
    [JsonPropertyName("messageRule")] public string MessageRule { get; set; } = "";
    [JsonPropertyName("verificationCode")] public string VerificationCode { get; set; } = "";
    [JsonPropertyName("popup")] public bool Popup { get; set; }
    [JsonPropertyName("popupType")] public string PopupType { get; set; } = "";
    [JsonPropertyName("source")] public string Source { get; set; } = "";
    [JsonPropertyName("createdAt")] public long CreatedAt { get; set; }
}


public sealed class LogEntryDto
{
    [JsonPropertyName("timestamp")] public string Timestamp { get; set; } = "";
    [JsonPropertyName("message")] public string Message { get; set; } = "";
}

public sealed class DiagnosticsSnapshotDto
{
    [JsonPropertyName("sampledAt")] public DateTimeOffset SampledAt { get; set; }
    [JsonPropertyName("status")] public AgentStatusDto Status { get; set; } = new();
    [JsonPropertyName("connections")] public List<ConnectionDto> Connections { get; set; } = [];
}
