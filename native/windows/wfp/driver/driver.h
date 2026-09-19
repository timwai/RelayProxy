#pragma once

#include <ntifs.h>
#include <ndis.h>
#include <fwpmk.h>
#include <fwpsk.h>
#include <ntstrsafe.h>
#include <ws2def.h>
#include <ws2ipdef.h>
#include <wdmsec.h>

#include "../include/relayproxy_wfp.h"

#define RP_TAG_FLOW  'fWpR'
#define RP_TAG_EVENT 'eWpR'
#define RP_TAG_CTX   'cWpR'
#define RP_TAG_UDP   'uWpR'
#define RP_TAG_REPLAY 'rWpR'

#define RP_MAX_EVENTS 4096u
#define RP_FLOW_TTL_100NS (5ull * 60ull * 10000000ull)

typedef struct _RP_FLOW_KEY {
    UINT64 ProcessId;
    UCHAR Protocol;
    UCHAR Family;
    UINT16 SourcePort;
    UINT16 DestinationPort;
    UCHAR SourceAddress[16];
    UCHAR DestinationAddress[16];
} RP_FLOW_KEY;

typedef struct _RP_FLOW {
    LIST_ENTRY Link;
    RP_FLOW_KEY Key;
    UINT64 RequestId;
    UINT64 AssociationId;
    UINT64 ControllerGeneration;
    UINT32 Action;
    UINT32 DecisionFlags;
    HANDLE CompletionContext;
    UINT32 CompartmentId;
    UINT32 InterfaceIndex;
    UINT32 SubInterfaceIndex;
    UINT64 LastSeen100ns;
    volatile LONG64 UploadBytes;
    volatile LONG64 DownloadBytes;
    LONG FlowAssociated;
    NET_BUFFER_LIST* PendingUdpNbl;
    UINT64 PendingUdpEndpointHandle;
    SCOPE_ID PendingUdpRemoteScopeId;
    UCHAR* PendingUdpControlData;
    ULONG PendingUdpControlDataLength;
    volatile LONG PendingUdpReplay;
    volatile LONG RefCount;
    volatile LONG Removed;
} RP_FLOW;

typedef struct _RP_EVENT_NODE {
    LIST_ENTRY Link;
    ULONG Size;
    UCHAR Data[1];
} RP_EVENT_NODE;

typedef struct _RP_DRIVER_STATE {
    KSPIN_LOCK Lock;
    LIST_ENTRY Flows;
    LIST_ENTRY Events;
    ULONG EventCount;
    volatile LONG64 NextId;
    UINT64 ControllerGeneration;

    BOOLEAN ControllerActive;
    BOOLEAN ControllerFailingOpen;
    BOOLEAN ProxyReady;
    ULONG ControllerPid;
    PFILE_OBJECT ControllerFileObject;
    USHORT TcpPortV4;
    USHORT TcpPortV6;
    ULONG HeartbeatMs;
    UINT64 LastHeartbeat100ns;
    KEVENT WatchdogStopEvent;
    HANDLE WatchdogThread;

    HANDLE EngineHandle;
    HANDLE RedirectHandle;
    HANDLE InjectionHandleV4;
    HANDLE InjectionHandleV6;
    HANDLE ReplayInjectionHandleV4;
    HANDLE ReplayInjectionHandleV6;
    NDIS_HANDLE NblPool;

    UINT32 AuthV4Id;
    UINT32 AuthV6Id;
    UINT32 RedirectV4Id;
    UINT32 RedirectV6Id;
    UINT32 DatagramV4Id;
    UINT32 DatagramV6Id;
    UINT32 FlowV4Id;
    UINT32 FlowV6Id;
    UINT32 StreamV4Id;
    UINT32 StreamV6Id;
} RP_DRIVER_STATE;

extern RP_DRIVER_STATE g_RpState;
extern PDEVICE_OBJECT g_RpDevice;

extern const GUID RP_PROVIDER_GUID;
extern const GUID RP_SUBLAYER_GUID;
extern const GUID RP_DEVICE_CLASS_GUID;
extern const GUID RP_CALLOUT_AUTH_V4;
extern const GUID RP_CALLOUT_AUTH_V6;
extern const GUID RP_CALLOUT_REDIRECT_V4;
extern const GUID RP_CALLOUT_REDIRECT_V6;
extern const GUID RP_CALLOUT_DATAGRAM_V4;
extern const GUID RP_CALLOUT_DATAGRAM_V6;
extern const GUID RP_CALLOUT_FLOW_V4;
extern const GUID RP_CALLOUT_FLOW_V6;
extern const GUID RP_CALLOUT_STREAM_V4;
extern const GUID RP_CALLOUT_STREAM_V6;

NTSTATUS RpStateInitialize(VOID);
VOID RpStateShutdown(VOID);
VOID RpControllerFailOpen(VOID);
BOOLEAN RpControllerHealthy(VOID);
NTSTATUS RpConfigureController(_In_ PIRP Irp, _In_ const RP_WFP_CONFIG* Config);
VOID RpControllerCleanup(_In_opt_ PFILE_OBJECT FileObject);
BOOLEAN RpIsControllerFile(_In_opt_ PFILE_OBJECT FileObject);
NTSTATUS RpHeartbeat(_In_ PFILE_OBJECT FileObject, _In_ ULONG RequestorPid);
NTSTATUS RpStopController(_In_opt_ PFILE_OBJECT FileObject, _In_ ULONG RequestorPid);
UINT64 RpCurrentControllerGeneration(VOID);
BOOLEAN RpControllerOwnsFlow(_In_ PFILE_OBJECT FileObject, _In_ const RP_FLOW* Flow);

RP_FLOW* RpFindFlowByRequestId(_In_ UINT64 RequestId);
RP_FLOW* RpFindFlowByAssociationId(_In_ UINT64 AssociationId);
RP_FLOW* RpFindFlowByKey(_In_ const RP_FLOW_KEY* Key);
NTSTATUS RpInsertPendingFlow(_In_ RP_FLOW* Flow);
VOID RpRemoveFlow(_In_ RP_FLOW* Flow, _In_ BOOLEAN QueueClose);
VOID RpTouchFlow(_In_ RP_FLOW* Flow);
VOID RpReferenceFlow(_In_ RP_FLOW* Flow);
VOID RpDereferenceFlow(_In_ RP_FLOW* Flow);
UINT64 RpNextId(VOID);

NTSTATUS RpQueueFlowEvent(_In_ const RP_FLOW* Flow, _In_opt_ const FWP_BYTE_BLOB* ProcessPath, _In_ ULONG Flags);
NTSTATUS RpQueueDatagramEvent(_In_ UINT32 Kind, _In_ const RP_FLOW* Flow, _In_ ULONG Flags, _In_reads_bytes_opt_(PayloadLength) const UCHAR* Payload, _In_ ULONG PayloadLength);
NTSTATUS RpReadEvent(_In_ PFILE_OBJECT FileObject, _Out_writes_bytes_(OutputLength) VOID* Output, _In_ ULONG OutputLength, _Out_ ULONG_PTR* BytesWritten);
NTSTATUS RpApplyDecision(_In_ PFILE_OBJECT FileObject, _In_ const RP_WFP_DECISION* Decision);
NTSTATUS RpSetProxyReady(_In_ PFILE_OBJECT FileObject, _In_ const RP_WFP_PROXY_READY* Ready);

NTSTATUS RpWfpStart(_In_ PDEVICE_OBJECT DeviceObject);
VOID RpWfpStop(VOID);

NTSTATUS RpInjectUdp(_In_ PFILE_OBJECT FileObject, _In_ const RP_WFP_UDP_INJECT* Request, _In_ ULONG InputLength);
NTSTATUS RpCapturePendingUdp(
    _Inout_ RP_FLOW* Flow,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _In_ NET_BUFFER_LIST* NetBufferList);
NTSTATUS RpReplayPendingUdp(_Inout_ RP_FLOW* Flow);
VOID RpReleasePendingUdp(_Inout_ RP_FLOW* Flow);

BOOLEAN RpExtractAleKey(_In_ const FWPS_INCOMING_VALUES0* Values, _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata, _Out_ RP_FLOW_KEY* Key, _Out_ UINT32* CompartmentId);
BOOLEAN RpExtractDatagramKey(_In_ const FWPS_INCOMING_VALUES0* Values, _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata, _Out_ RP_FLOW_KEY* Key);
BOOLEAN RpAddressIsLoopback(_In_ UCHAR Family, _In_reads_(16) const UCHAR Address[16]);
VOID RpFillEventAddress(_Out_writes_(16) UCHAR Dst[16], _In_ UCHAR Family, _In_ const FWP_VALUE0* Value);
