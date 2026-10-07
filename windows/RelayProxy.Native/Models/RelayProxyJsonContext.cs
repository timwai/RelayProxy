using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.Json.Serialization.Metadata;

namespace RelayProxy.Native.Models;

[JsonSourceGenerationOptions(
    GenerationMode = JsonSourceGenerationMode.Metadata,
    PropertyNameCaseInsensitive = true)]
[JsonSerializable(typeof(AgentStatusDto))]
[JsonSerializable(typeof(AgentConfigDto))]
[JsonSerializable(typeof(List<ProxyExitDto>))]
[JsonSerializable(typeof(TrafficSnapshotDto))]
[JsonSerializable(typeof(List<PushMessageDto>))]
[JsonSerializable(typeof(List<RdpTargetDto>))]
[JsonSerializable(typeof(List<LogEntryDto>))]
[JsonSerializable(typeof(DiagnosticsSnapshotDto))]
[JsonSerializable(typeof(List<DiagnosticsSnapshotDto>))]
[JsonSerializable(typeof(NetworkServiceStatusDto))]
[JsonSerializable(typeof(SaveResultDto))]
[JsonSerializable(typeof(SpeedTestApiResponseDto))]
[JsonSerializable(typeof(RdpConnectResponseDto))]
[JsonSerializable(typeof(MutationMessageDto))]
[JsonSerializable(typeof(ServerConsoleLoginResponseDto))]
[JsonSerializable(typeof(List<MessageChannelDto>))]
[JsonSerializable(typeof(MessageChannelDto))]
[JsonSerializable(typeof(EmptyRequestDto))]
[JsonSerializable(typeof(SpeedTestRequestDto))]
[JsonSerializable(typeof(SelectExitRequestDto))]
[JsonSerializable(typeof(ProxyPauseRequestDto))]
[JsonSerializable(typeof(RdpConnectRequestDto))]
[JsonSerializable(typeof(AutostartRequestDto))]
[JsonSerializable(typeof(ServerConsoleLoginRequestDto))]
[JsonSerializable(typeof(MessageChannelUpdateRequestDto))]
[JsonSerializable(typeof(AgentConfigUpdateDto))]
[JsonSerializable(typeof(DiagnosticsManifestDto))]
public partial class RelayProxyJsonContext : JsonSerializerContext
{
    public static JsonTypeInfo<T> Get<T>() =>
        (JsonTypeInfo<T>)(Default.GetTypeInfo(typeof(T))
            ?? throw new InvalidOperationException($"JSON metadata is not registered for {typeof(T).FullName}."));
}
