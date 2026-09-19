#pragma once

#include <ntddk.h>

#define RP_WFP_ABI_VERSION 1u
#define RP_WFP_MAX_PATH_CHARS 520u
#define RP_WFP_MAX_UDP_PAYLOAD 65507u

#define RP_WFP_FEATURE_TCP             (1ull << 0)
#define RP_WFP_FEATURE_UDP             (1ull << 1)
#define RP_WFP_FEATURE_IPV6            (1ull << 2)
#define RP_WFP_FEATURE_SYSTEM_IDENTITY (1ull << 3)

#define RP_WFP_EVENT_FLOW      1u
#define RP_WFP_EVENT_UDP_DATA  2u
#define RP_WFP_EVENT_DNS       3u
#define RP_WFP_EVENT_CLOSE     4u

#define RP_WFP_EVENT_FLAG_SYSTEM   (1u << 0)
#define RP_WFP_EVENT_FLAG_OUTBOUND (1u << 1)

#define RP_WFP_ACTION_DIRECT 1u
#define RP_WFP_ACTION_PROXY  2u
#define RP_WFP_ACTION_REJECT 3u

#define RP_WFP_DEVICE_TYPE 0x8000u
#define RP_WFP_IOCTL_GET_VERSION  CTL_CODE(RP_WFP_DEVICE_TYPE, 0x800, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_SET_CONFIG   CTL_CODE(RP_WFP_DEVICE_TYPE, 0x801, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_GET_EVENT    CTL_CODE(RP_WFP_DEVICE_TYPE, 0x802, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_SET_DECISION CTL_CODE(RP_WFP_DEVICE_TYPE, 0x803, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_INJECT_UDP   CTL_CODE(RP_WFP_DEVICE_TYPE, 0x804, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_HEARTBEAT    CTL_CODE(RP_WFP_DEVICE_TYPE, 0x805, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_STOP         CTL_CODE(RP_WFP_DEVICE_TYPE, 0x806, METHOD_BUFFERED, FILE_ANY_ACCESS)
#define RP_WFP_IOCTL_RELEASE      CTL_CODE(RP_WFP_DEVICE_TYPE, 0x807, METHOD_BUFFERED, FILE_ANY_ACCESS)

#pragma pack(push, 1)

typedef struct _RP_WFP_VERSION {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT64 Features;
} RP_WFP_VERSION;

typedef struct _RP_WFP_CONFIG {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT32 ControllerPid;
    UINT16 TcpPortV4;
    UINT16 TcpPortV6;
    UINT32 HeartbeatMs;
    UINT32 Reserved;
} RP_WFP_CONFIG;

typedef struct _RP_WFP_DECISION {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT64 RequestId;
    UINT32 Action;
    UINT32 Reserved;
} RP_WFP_DECISION;

typedef struct _RP_WFP_RELEASE {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT64 RequestId;
} RP_WFP_RELEASE;

typedef struct _RP_WFP_UDP_INJECT {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT64 AssociationId;
    UINT32 PayloadLength;
    UINT32 Reserved;
    UCHAR Payload[1];
} RP_WFP_UDP_INJECT;

typedef struct _RP_WFP_EVENT {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT32 Kind;
    UINT32 Flags;
    UINT64 RequestId;
    UINT64 AssociationId;
    UINT64 ProcessId;
    UINT32 CompartmentId;
    UCHAR Protocol;
    UCHAR Family;
    UINT16 SourcePort;
    UINT16 DestinationPort;
    UINT16 ProcessPathChars;
    UINT32 PayloadLength;
    UCHAR SourceAddress[16];
    UCHAR DestinationAddress[16];
    WCHAR ProcessPath[RP_WFP_MAX_PATH_CHARS];
    UCHAR Payload[1];
} RP_WFP_EVENT;

typedef struct _RP_WFP_REDIRECT_CONTEXT {
    UINT32 AbiVersion;
    UINT32 Size;
    UINT64 RequestId;
} RP_WFP_REDIRECT_CONTEXT;

#pragma pack(pop)

C_ASSERT(FIELD_OFFSET(RP_WFP_EVENT, Payload) == 1128);
C_ASSERT(sizeof(RP_WFP_VERSION) == 16);
C_ASSERT(sizeof(RP_WFP_CONFIG) == 24);
C_ASSERT(sizeof(RP_WFP_DECISION) == 24);
C_ASSERT(sizeof(RP_WFP_RELEASE) == 16);
C_ASSERT(FIELD_OFFSET(RP_WFP_UDP_INJECT, Payload) == 24);
C_ASSERT(sizeof(RP_WFP_REDIRECT_CONTEXT) == 16);
