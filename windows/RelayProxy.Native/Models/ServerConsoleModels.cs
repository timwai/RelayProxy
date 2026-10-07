using System.Text.Json.Serialization;

namespace RelayProxy.Native.Models;

public sealed class ServerConsoleLoginResponseDto
{
    [JsonPropertyName("user")] public ServerConsoleUserDto User { get; set; } = new();
}

public sealed class ServerConsoleUserDto
{
    [JsonPropertyName("id")] public string Id { get; set; } = "";
    [JsonPropertyName("identityId")] public string IdentityId { get; set; } = "";
    [JsonPropertyName("username")] public string Username { get; set; } = "";
    [JsonPropertyName("displayName")] public string DisplayName { get; set; } = "";
    [JsonPropertyName("role")] public string Role { get; set; } = "";
    [JsonPropertyName("status")] public string Status { get; set; } = "";
}

public sealed class MessageChannelDto
{
    [JsonPropertyName("id")] public string Id { get; set; } = "";
    [JsonPropertyName("identityId")] public string IdentityId { get; set; } = "";
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("allDevices")] public bool AllDevices { get; set; }
    [JsonPropertyName("deviceIds")] public List<string> DeviceIds { get; set; } = [];
    [JsonPropertyName("messageRules")] public List<MessageRuleDto> MessageRules { get; set; } = [];
    [JsonPropertyName("useDefaultVerification")] public bool UseDefaultVerification { get; set; }
    [JsonPropertyName("createdAt")] public DateTimeOffset CreatedAt { get; set; }
    [JsonPropertyName("updatedAt")] public DateTimeOffset UpdatedAt { get; set; }
}

public sealed class MessageRuleDto
{
    [JsonPropertyName("name")] public string Name { get; set; } = "";
    [JsonPropertyName("type")] public string Type { get; set; } = "message";
    [JsonPropertyName("enabled")] public bool Enabled { get; set; } = true;
    [JsonPropertyName("default")] public bool Default { get; set; }
    [JsonPropertyName("match")] public MessageMatchDto Match { get; set; } = new();
    [JsonPropertyName("verification")] public VerificationExtractorDto? Verification { get; set; }
    [JsonPropertyName("popup")] public bool? Popup { get; set; } = true;

    public MessageRuleDto Clone() => new()
    {
        Name = Name,
        Type = Type,
        Enabled = Enabled,
        Default = Default,
        Popup = Popup,
        Match = Match.Clone(),
        Verification = Verification?.Clone()
    };
}

public sealed class MessageMatchDto
{
    [JsonPropertyName("matchType")] public string MatchType { get; set; } = "all";
    [JsonPropertyName("keywords")] public List<string> Keywords { get; set; } = [];
    [JsonPropertyName("keywordMode")] public string KeywordMode { get; set; } = "any";
    [JsonPropertyName("pattern")] public string Pattern { get; set; } = "";
    [JsonPropertyName("caseSensitive")] public bool CaseSensitive { get; set; }

    public MessageMatchDto Clone() => new()
    {
        MatchType = MatchType,
        Keywords = [.. Keywords],
        KeywordMode = KeywordMode,
        Pattern = Pattern,
        CaseSensitive = CaseSensitive
    };
}

public sealed class VerificationExtractorDto
{
    [JsonPropertyName("type")] public string Type { get; set; } = "auto";
    [JsonPropertyName("pattern")] public string Pattern { get; set; } = "";
    [JsonPropertyName("maxDistance")] public int MaxDistance { get; set; } = 64;
    [JsonPropertyName("minLength")] public int MinLength { get; set; } = 4;
    [JsonPropertyName("maxLength")] public int MaxLength { get; set; } = 8;
    [JsonPropertyName("allowLetters")] public bool AllowLetters { get; set; } = true;
    [JsonPropertyName("allowDigits")] public bool AllowDigits { get; set; } = true;
    [JsonPropertyName("requireDigit")] public bool RequireDigit { get; set; } = true;

    public VerificationExtractorDto Clone() => new()
    {
        Type = Type,
        Pattern = Pattern,
        MaxDistance = MaxDistance,
        MinLength = MinLength,
        MaxLength = MaxLength,
        AllowLetters = AllowLetters,
        AllowDigits = AllowDigits,
        RequireDigit = RequireDigit
    };
}
