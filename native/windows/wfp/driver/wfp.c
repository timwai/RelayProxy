#include "driver.h"

#define RP_IPPROTO_TCP 6u
#define RP_IPPROTO_UDP 17u
#define RP_UDP_HEADER_SIZE 8u

static NTSTATUS NTAPI RpNotify(
    _In_ FWPS_CALLOUT_NOTIFY_TYPE NotifyType,
    _In_ const GUID* FilterKey,
    _Inout_ FWPS_FILTER1* Filter)
{
    UNREFERENCED_PARAMETER(NotifyType);
    UNREFERENCED_PARAMETER(FilterKey);
    UNREFERENCED_PARAMETER(Filter);
    return STATUS_SUCCESS;
}

static VOID RpSetV4Address(_Out_writes_(16) UCHAR Dst[16], _In_ UINT32 Address)
{
    RtlZeroMemory(Dst, 16);
    Dst[0] = (UCHAR)((Address >> 24) & 0xff);
    Dst[1] = (UCHAR)((Address >> 16) & 0xff);
    Dst[2] = (UCHAR)((Address >> 8) & 0xff);
    Dst[3] = (UCHAR)(Address & 0xff);
}

VOID RpFillEventAddress(_Out_writes_(16) UCHAR Dst[16], _In_ UCHAR Family, _In_ const FWP_VALUE0* Value)
{
    RtlZeroMemory(Dst, 16);
    if (Value == NULL) {
        return;
    }
    if (Family == 4 && Value->type == FWP_UINT32) {
        RpSetV4Address(Dst, Value->uint32);
    } else if (Family == 6 &&
               Value->type == FWP_BYTE_ARRAY16_TYPE &&
               Value->byteArray16 != NULL) {
        RtlCopyMemory(Dst, Value->byteArray16->byteArray16, 16);
    }
}

BOOLEAN RpAddressIsLoopback(_In_ UCHAR Family, _In_reads_(16) const UCHAR Address[16])
{
    ULONG index;
    if (Family == 4) {
        return Address[0] == 127;
    }
    if (Family != 6) {
        return FALSE;
    }
    for (index = 0; index < 15; index++) {
        if (Address[index] != 0) {
            return FALSE;
        }
    }
    return Address[15] == 1;
}

static BOOLEAN RpExtractKeyFields(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _In_ UINT32 LocalAddressIndex,
    _In_ UINT32 LocalPortIndex,
    _In_ UINT32 RemoteAddressIndex,
    _In_ UINT32 RemotePortIndex,
    _In_ UINT32 ProtocolIndex,
    _In_ UCHAR Family,
    _Out_ RP_FLOW_KEY* Key)
{
    const FWP_VALUE0* localAddress;
    const FWP_VALUE0* remoteAddress;
    const FWP_VALUE0* localPort;
    const FWP_VALUE0* remotePort;
    const FWP_VALUE0* protocol;

    if (Values == NULL || Metadata == NULL || Key == NULL) {
        return FALSE;
    }

    localAddress = &Values->incomingValue[LocalAddressIndex].value;
    remoteAddress = &Values->incomingValue[RemoteAddressIndex].value;
    localPort = &Values->incomingValue[LocalPortIndex].value;
    remotePort = &Values->incomingValue[RemotePortIndex].value;
    protocol = &Values->incomingValue[ProtocolIndex].value;

    if (localPort->type != FWP_UINT16 ||
        remotePort->type != FWP_UINT16 ||
        protocol->type != FWP_UINT8 ||
        (protocol->uint8 != RP_IPPROTO_TCP && protocol->uint8 != RP_IPPROTO_UDP)) {
        return FALSE;
    }

    RtlZeroMemory(Key, sizeof(*Key));
    Key->Family = Family;
    Key->Protocol = protocol->uint8;
    Key->SourcePort = localPort->uint16;
    Key->DestinationPort = remotePort->uint16;

    if (FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_PROCESS_ID)) {
        Key->ProcessId = Metadata->processId;
    }

    RpFillEventAddress(Key->SourceAddress, Family, localAddress);
    RpFillEventAddress(Key->DestinationAddress, Family, remoteAddress);
    return TRUE;
}

BOOLEAN RpExtractAleKey(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Out_ RP_FLOW_KEY* Key,
    _Out_ UINT32* CompartmentId)
{
    BOOLEAN result = FALSE;

    if (CompartmentId != NULL) {
        *CompartmentId = FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_COMPARTMENT_ID)
            ? Metadata->compartmentId
            : UNSPECIFIED_COMPARTMENT_ID;
    }

    switch (Values->layerId) {
    case FWPS_LAYER_ALE_AUTH_CONNECT_V4:
        result = RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_LOCAL_ADDRESS,
            FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_LOCAL_PORT,
            FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_REMOTE_ADDRESS,
            FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_REMOTE_PORT,
            FWPS_FIELD_ALE_AUTH_CONNECT_V4_IP_PROTOCOL,
            4, Key);
        break;
    case FWPS_LAYER_ALE_AUTH_CONNECT_V6:
        result = RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_LOCAL_ADDRESS,
            FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_LOCAL_PORT,
            FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_REMOTE_ADDRESS,
            FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_REMOTE_PORT,
            FWPS_FIELD_ALE_AUTH_CONNECT_V6_IP_PROTOCOL,
            6, Key);
        break;
    case FWPS_LAYER_ALE_CONNECT_REDIRECT_V4:
        result = RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_LOCAL_ADDRESS,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_LOCAL_PORT,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_REMOTE_ADDRESS,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_REMOTE_PORT,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V4_IP_PROTOCOL,
            4, Key);
        break;
    case FWPS_LAYER_ALE_CONNECT_REDIRECT_V6:
        result = RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_LOCAL_ADDRESS,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_LOCAL_PORT,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_REMOTE_ADDRESS,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_REMOTE_PORT,
            FWPS_FIELD_ALE_CONNECT_REDIRECT_V6_IP_PROTOCOL,
            6, Key);
        break;
    case FWPS_LAYER_ALE_FLOW_ESTABLISHED_V4:
        result = RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V4_IP_LOCAL_ADDRESS,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V4_IP_LOCAL_PORT,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V4_IP_REMOTE_ADDRESS,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V4_IP_REMOTE_PORT,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V4_IP_PROTOCOL,
            4, Key);
        break;
    case FWPS_LAYER_ALE_FLOW_ESTABLISHED_V6:
        result = RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V6_IP_LOCAL_ADDRESS,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V6_IP_LOCAL_PORT,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V6_IP_REMOTE_ADDRESS,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V6_IP_REMOTE_PORT,
            FWPS_FIELD_ALE_FLOW_ESTABLISHED_V6_IP_PROTOCOL,
            6, Key);
        break;
    default:
        break;
    }
    return result;
}

BOOLEAN RpExtractDatagramKey(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Out_ RP_FLOW_KEY* Key)
{
    if (Values->layerId == FWPS_LAYER_DATAGRAM_DATA_V4) {
        return RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_DATAGRAM_DATA_V4_IP_LOCAL_ADDRESS,
            FWPS_FIELD_DATAGRAM_DATA_V4_IP_LOCAL_PORT,
            FWPS_FIELD_DATAGRAM_DATA_V4_IP_REMOTE_ADDRESS,
            FWPS_FIELD_DATAGRAM_DATA_V4_IP_REMOTE_PORT,
            FWPS_FIELD_DATAGRAM_DATA_V4_IP_PROTOCOL,
            4, Key);
    }
    if (Values->layerId == FWPS_LAYER_DATAGRAM_DATA_V6) {
        return RpExtractKeyFields(
            Values, Metadata,
            FWPS_FIELD_DATAGRAM_DATA_V6_IP_LOCAL_ADDRESS,
            FWPS_FIELD_DATAGRAM_DATA_V6_IP_LOCAL_PORT,
            FWPS_FIELD_DATAGRAM_DATA_V6_IP_REMOTE_ADDRESS,
            FWPS_FIELD_DATAGRAM_DATA_V6_IP_REMOTE_PORT,
            FWPS_FIELD_DATAGRAM_DATA_V6_IP_PROTOCOL,
            6, Key);
    }
    return FALSE;
}

static UINT32 RpAleAuthFlags(_In_ const FWPS_INCOMING_VALUES0* Values)
{
    if (Values->layerId == FWPS_LAYER_ALE_AUTH_CONNECT_V4) {
        return Values->incomingValue[FWPS_FIELD_ALE_AUTH_CONNECT_V4_FLAGS].value.uint32;
    }
    return Values->incomingValue[FWPS_FIELD_ALE_AUTH_CONNECT_V6_FLAGS].value.uint32;
}

static VOID NTAPI RpAuthClassify(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Inout_opt_ VOID* LayerData,
    _In_opt_ const VOID* ClassifyContext,
    _In_ const FWPS_FILTER1* Filter,
    _In_ UINT64 FlowContext,
    _Inout_ FWPS_CLASSIFY_OUT0* ClassifyOut)
{
    RP_FLOW_KEY key;
    RP_FLOW* flow = NULL;
    UINT32 compartmentId = UNSPECIFIED_COMPARTMENT_ID;
    UINT32 flags;
    BOOLEAN reauthorize;
    HANDLE completionContext = NULL;
    NTSTATUS status;
    ULONG eventFlags = 0;
    const FWP_BYTE_BLOB* processPath = NULL;

    UNREFERENCED_PARAMETER(LayerData);
    UNREFERENCED_PARAMETER(ClassifyContext);
    UNREFERENCED_PARAMETER(Filter);
    UNREFERENCED_PARAMETER(FlowContext);

    if ((ClassifyOut->rights & FWPS_RIGHT_ACTION_WRITE) == 0) {
        return;
    }

    if (!RpExtractAleKey(Values, Metadata, &key, &compartmentId)) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    if (!RpControllerHealthy() ||
        key.ProcessId == g_RpState.ControllerPid ||
        RpAddressIsLoopback(key.Family, key.SourceAddress) ||
        RpAddressIsLoopback(key.Family, key.DestinationAddress)) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    flags = RpAleAuthFlags(Values);
    reauthorize = (flags & FWP_CONDITION_FLAG_IS_REAUTHORIZE) != 0;

    flow = RpFindFlowByKey(&key);
    if (reauthorize) {
        if (flow == NULL) {
            ClassifyOut->actionType = FWP_ACTION_PERMIT;
            return;
        }
        RpTouchFlow(flow);
        if (flow->Action == RP_WFP_ACTION_REJECT) {
            ClassifyOut->actionType = FWP_ACTION_BLOCK;
            ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
            RpRemoveFlow(flow, TRUE);
        } else {
            ClassifyOut->actionType = FWP_ACTION_PERMIT;
            if (flow->Key.Protocol == RP_IPPROTO_UDP) {
                (VOID)RpReplayPendingUdp(flow);
            }
        }
        RpDereferenceFlow(flow);
        return;
    }

    if (flow != NULL) {
        /* A duplicate initial authorization can race before reauthorization. */
        if (flow->Action == RP_WFP_ACTION_REJECT) {
            ClassifyOut->actionType = FWP_ACTION_BLOCK;
            ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
        } else if (flow->Action == RP_WFP_ACTION_DIRECT ||
                   flow->Action == RP_WFP_ACTION_PROXY) {
            ClassifyOut->actionType = FWP_ACTION_PERMIT;
        } else {
            ClassifyOut->actionType = FWP_ACTION_BLOCK;
            ClassifyOut->flags |= FWPS_CLASSIFY_OUT_FLAG_ABSORB;
            ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
        }
        RpDereferenceFlow(flow);
        return;
    }

    if (!FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_COMPLETION_HANDLE)) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    flow = (RP_FLOW*)ExAllocatePool2(POOL_FLAG_NON_PAGED, sizeof(*flow), RP_TAG_FLOW);
    if (flow == NULL) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }
    RtlZeroMemory(flow, sizeof(*flow));
    flow->Key = key;
    flow->RequestId = RpNextId();
    flow->AssociationId = key.Protocol == RP_IPPROTO_UDP ? flow->RequestId : 0;
    flow->CompartmentId = compartmentId;

    /*
     * FwpsPendOperation flushes non-TCP packet data when the ALE operation is
     * completed. Reference the initial UDP NBL before pending so we can clone
     * and replay it after reauthorization instead of relying on application
     * retransmission. If capture is not possible, fail open before pending.
     */
    if (key.Protocol == RP_IPPROTO_UDP && LayerData != NULL) {
        status = RpCapturePendingUdp(
            flow,
            Metadata,
            (NET_BUFFER_LIST*)LayerData);
        if (!NT_SUCCESS(status)) {
            RpReleasePendingUdp(flow);
            ExFreePoolWithTag(flow, RP_TAG_FLOW);
            ClassifyOut->actionType = FWP_ACTION_PERMIT;
            return;
        }
    }

    status = FwpsPendOperation0(Metadata->completionHandle, &completionContext);
    if (!NT_SUCCESS(status)) {
        RpReleasePendingUdp(flow);
        ExFreePoolWithTag(flow, RP_TAG_FLOW);
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }
    flow->CompletionContext = completionContext;

    status = RpInsertPendingFlow(flow);
    if (!NT_SUCCESS(status)) {
        RpReleasePendingUdp(flow);
        ExFreePoolWithTag(flow, RP_TAG_FLOW);
        FwpsCompleteOperation0(completionContext, NULL);
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    if (key.ProcessId == 4) {
        eventFlags |= RP_WFP_EVENT_FLAG_SYSTEM;
    }
    if (FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_PROCESS_PATH)) {
        processPath = Metadata->processPath;
    }

    status = RpQueueFlowEvent(flow, processPath, eventFlags | RP_WFP_EVENT_FLAG_OUTBOUND);
    if (!NT_SUCCESS(status)) {
        flow->Action = RP_WFP_ACTION_DIRECT;
        flow->CompletionContext = NULL;
        FwpsCompleteOperation0(completionContext, NULL);
    }

    ClassifyOut->actionType = FWP_ACTION_BLOCK;
    ClassifyOut->flags |= FWPS_CLASSIFY_OUT_FLAG_ABSORB;
    ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
}

static VOID RpSetLoopbackTarget(_Inout_ FWPS_CONNECT_REQUEST0* Request, _In_ UCHAR Family, _In_ USHORT Port)
{
    if (Family == 4) {
        SOCKADDR_IN* address = (SOCKADDR_IN*)&Request->remoteAddressAndPort;
        RtlZeroMemory(address, sizeof(*address));
        address->sin_family = AF_INET;
        address->sin_port = RtlUshortByteSwap(Port);
        address->sin_addr.S_un.S_addr = RtlUlongByteSwap(0x7f000001u);
    } else {
        SOCKADDR_IN6* address = (SOCKADDR_IN6*)&Request->remoteAddressAndPort;
        RtlZeroMemory(address, sizeof(*address));
        address->sin6_family = AF_INET6;
        address->sin6_port = RtlUshortByteSwap(Port);
        address->sin6_addr.u.Byte[15] = 1;
    }
}

static VOID NTAPI RpRedirectClassify(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Inout_opt_ VOID* LayerData,
    _In_opt_ const VOID* ClassifyContext,
    _In_ const FWPS_FILTER1* Filter,
    _In_ UINT64 FlowContext,
    _Inout_ FWPS_CLASSIFY_OUT0* ClassifyOut)
{
    RP_FLOW_KEY key;
    RP_FLOW* flow = NULL;
    UINT32 compartmentId;
    UINT64 classifyHandle = 0;
    FWPS_CONNECT_REQUEST0* request = NULL;
    RP_WFP_REDIRECT_CONTEXT* redirectContext = NULL;
    NTSTATUS status;
    FWPS_CONNECTION_REDIRECT_STATE redirectState;

    UNREFERENCED_PARAMETER(LayerData);
    UNREFERENCED_PARAMETER(Filter);
    UNREFERENCED_PARAMETER(FlowContext);

    if ((ClassifyOut->rights & FWPS_RIGHT_ACTION_WRITE) == 0) {
        return;
    }
    if (!RpControllerHealthy() ||
        !RpExtractAleKey(Values, Metadata, &key, &compartmentId) ||
        key.Protocol != RP_IPPROTO_TCP ||
        key.ProcessId == g_RpState.ControllerPid) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    if (FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_REDIRECT_RECORD_HANDLE)) {
        redirectState = FwpsQueryConnectionRedirectState0(
            Metadata->redirectRecords,
            g_RpState.RedirectHandle,
            NULL);
        if (redirectState == FWPS_CONNECTION_REDIRECTED_BY_SELF ||
            redirectState == FWPS_CONNECTION_PREVIOUSLY_REDIRECTED_BY_SELF) {
            ClassifyOut->actionType = FWP_ACTION_PERMIT;
            return;
        }
    }

    flow = RpFindFlowByKey(&key);
    if (flow == NULL || flow->Action != RP_WFP_ACTION_PROXY) {
        if (flow != NULL) {
            RpDereferenceFlow(flow);
        }
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    status = FwpsAcquireClassifyHandle0((VOID*)ClassifyContext, 0, &classifyHandle);
    if (!NT_SUCCESS(status)) {
        goto Block;
    }

    status = FwpsAcquireWritableLayerDataPointer0(
        classifyHandle,
        Filter->filterId,
        0,
        (PVOID*)&request,
        ClassifyOut);
    if (!NT_SUCCESS(status) || request == NULL) {
        goto Block;
    }

    redirectContext = (RP_WFP_REDIRECT_CONTEXT*)ExAllocatePool2(
        POOL_FLAG_NON_PAGED,
        sizeof(*redirectContext),
        RP_TAG_CTX);
    if (redirectContext == NULL) {
        status = STATUS_INSUFFICIENT_RESOURCES;
        goto Block;
    }
    redirectContext->AbiVersion = RP_WFP_ABI_VERSION;
    redirectContext->Size = sizeof(*redirectContext);
    redirectContext->RequestId = flow->RequestId;

    request->localRedirectTargetPID = g_RpState.ControllerPid;
    request->localRedirectHandle = g_RpState.RedirectHandle;
    request->localRedirectContext = redirectContext;
    request->localRedirectContextSize = sizeof(*redirectContext);
    RpSetLoopbackTarget(
        request,
        key.Family,
        key.Family == 4 ? g_RpState.TcpPortV4 : g_RpState.TcpPortV6);

    FwpsApplyModifiedLayerData0(classifyHandle, request, 0);
    redirectContext = NULL; /* WFP owns redirect context after apply. */

    FwpsReleaseClassifyHandle0(classifyHandle);
    RpDereferenceFlow(flow);
    ClassifyOut->actionType = FWP_ACTION_PERMIT;
    ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
    return;

Block:
    if (redirectContext != NULL) {
        ExFreePoolWithTag(redirectContext, RP_TAG_CTX);
    }
    if (classifyHandle != 0) {
        FwpsReleaseClassifyHandle0(classifyHandle);
    }
    RpDereferenceFlow(flow);
    ClassifyOut->actionType = FWP_ACTION_BLOCK;
    ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
}

static VOID NTAPI RpFlowDelete(
    _In_ UINT16 LayerId,
    _In_ UINT32 CalloutId,
    _In_ UINT64 FlowContext)
{
    RP_FLOW* flow = (RP_FLOW*)(ULONG_PTR)FlowContext;
    UNREFERENCED_PARAMETER(LayerId);
    UNREFERENCED_PARAMETER(CalloutId);
    if (flow == NULL) {
        return;
    }
    RpRemoveFlow(flow, TRUE);
    RpDereferenceFlow(flow); /* flow association reference */
}

static VOID NTAPI RpFlowEstablishedClassify(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Inout_opt_ VOID* LayerData,
    _In_opt_ const VOID* ClassifyContext,
    _In_ const FWPS_FILTER1* Filter,
    _In_ UINT64 FlowContext,
    _Inout_ FWPS_CLASSIFY_OUT0* ClassifyOut)
{
    RP_FLOW_KEY key;
    RP_FLOW* flow;
    UINT32 compartmentId;
    UINT16 targetLayer;
    UINT32 targetCallout;
    NTSTATUS status;

    UNREFERENCED_PARAMETER(LayerData);
    UNREFERENCED_PARAMETER(ClassifyContext);
    UNREFERENCED_PARAMETER(Filter);
    UNREFERENCED_PARAMETER(FlowContext);

    ClassifyOut->actionType = FWP_ACTION_PERMIT;

    if (!FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_FLOW_HANDLE) ||
        !RpExtractAleKey(Values, Metadata, &key, &compartmentId)) {
        return;
    }

    flow = RpFindFlowByKey(&key);
    if (flow == NULL) {
        return;
    }

    if (key.Protocol == RP_IPPROTO_TCP) {
        if (flow->Action != RP_WFP_ACTION_DIRECT) {
            RpDereferenceFlow(flow);
            return;
        }
        targetLayer = key.Family == 4 ? FWPS_LAYER_STREAM_V4 : FWPS_LAYER_STREAM_V6;
        targetCallout = key.Family == 4 ? g_RpState.StreamV4Id : g_RpState.StreamV6Id;
    } else {
        if (flow->Action != RP_WFP_ACTION_DIRECT &&
            flow->Action != RP_WFP_ACTION_PROXY) {
            RpDereferenceFlow(flow);
            return;
        }
        targetLayer = key.Family == 4 ? FWPS_LAYER_DATAGRAM_DATA_V4 : FWPS_LAYER_DATAGRAM_DATA_V6;
        targetCallout = key.Family == 4 ? g_RpState.DatagramV4Id : g_RpState.DatagramV6Id;
    }

    if (InterlockedCompareExchange(&flow->FlowAssociated, 1, 0) != 0) {
        RpDereferenceFlow(flow);
        return;
    }

    RpReferenceFlow(flow); /* association reference, released in RpFlowDelete */
    status = FwpsFlowAssociateContext0(
        Metadata->flowHandle,
        targetLayer,
        targetCallout,
        (UINT64)(ULONG_PTR)flow);
    if (!NT_SUCCESS(status)) {
        InterlockedExchange(&flow->FlowAssociated, 0);
        RpDereferenceFlow(flow);
    }
    RpDereferenceFlow(flow);
}

static ULONG RpDatagramDirection(_In_ const FWPS_INCOMING_VALUES0* Values)
{
    if (Values->layerId == FWPS_LAYER_DATAGRAM_DATA_V4) {
        return Values->incomingValue[FWPS_FIELD_DATAGRAM_DATA_V4_DIRECTION].value.uint32;
    }
    return Values->incomingValue[FWPS_FIELD_DATAGRAM_DATA_V6_DIRECTION].value.uint32;
}

static VOID RpRememberDatagramInterface(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Inout_ RP_FLOW* Flow)
{
    if (FWPS_IS_METADATA_FIELD_PRESENT(Metadata, FWPS_METADATA_FIELD_COMPARTMENT_ID)) {
        Flow->CompartmentId = Metadata->compartmentId;
    }
    if (Values->layerId == FWPS_LAYER_DATAGRAM_DATA_V4) {
        Flow->InterfaceIndex = Values->incomingValue[FWPS_FIELD_DATAGRAM_DATA_V4_INTERFACE_INDEX].value.uint32;
        Flow->SubInterfaceIndex = Values->incomingValue[FWPS_FIELD_DATAGRAM_DATA_V4_SUB_INTERFACE_INDEX].value.uint32;
    } else {
        Flow->InterfaceIndex = Values->incomingValue[FWPS_FIELD_DATAGRAM_DATA_V6_INTERFACE_INDEX].value.uint32;
        Flow->SubInterfaceIndex = Values->incomingValue[FWPS_FIELD_DATAGRAM_DATA_V6_SUB_INTERFACE_INDEX].value.uint32;
    }
}

static NTSTATUS RpQueueNetBufferPayload(
    _In_ UINT32 Kind,
    _In_ RP_FLOW* Flow,
    _In_ ULONG Flags,
    _In_ NET_BUFFER* NetBuffer,
    _In_ ULONG SkipBytes)
{
    ULONG totalLength;
    ULONG payloadLength;
    PUCHAR direct;
    PUCHAR scratch = NULL;
    NTSTATUS status;

    totalLength = NET_BUFFER_DATA_LENGTH(NetBuffer);
    if (totalLength <= SkipBytes) {
        return STATUS_INVALID_BUFFER_SIZE;
    }

    if (SkipBytes != 0) {
        NdisAdvanceNetBufferDataStart(NetBuffer, SkipBytes, FALSE, NULL);
    }
    payloadLength = NET_BUFFER_DATA_LENGTH(NetBuffer);
    if (payloadLength > RP_WFP_MAX_UDP_PAYLOAD) {
        status = STATUS_INVALID_BUFFER_SIZE;
        goto Exit;
    }

    direct = (PUCHAR)NdisGetDataBuffer(NetBuffer, payloadLength, NULL, 1, 0);
    if (direct == NULL) {
        scratch = (PUCHAR)ExAllocatePool2(POOL_FLAG_NON_PAGED, payloadLength, RP_TAG_UDP);
        if (scratch == NULL) {
            status = STATUS_INSUFFICIENT_RESOURCES;
            goto Exit;
        }
        direct = (PUCHAR)NdisGetDataBuffer(NetBuffer, payloadLength, scratch, 1, 0);
        if (direct == NULL) {
            status = STATUS_DATA_ERROR;
            goto Exit;
        }
    }

    status = RpQueueDatagramEvent(Kind, Flow, Flags, direct, payloadLength);

Exit:
    if (scratch != NULL) {
        ExFreePoolWithTag(scratch, RP_TAG_UDP);
    }
    if (SkipBytes != 0) {
        (VOID)NdisRetreatNetBufferDataStart(NetBuffer, SkipBytes, 0, NULL);
    }
    return status;
}

static VOID NTAPI RpDatagramClassify(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Inout_opt_ VOID* LayerData,
    _In_opt_ const VOID* ClassifyContext,
    _In_ const FWPS_FILTER1* Filter,
    _In_ UINT64 FlowContext,
    _Inout_ FWPS_CLASSIFY_OUT0* ClassifyOut)
{
    RP_FLOW* flow = (RP_FLOW*)(ULONG_PTR)FlowContext;
    RP_FLOW_KEY fallbackKey;
    BOOLEAN referencedFlow = FALSE;
    NET_BUFFER_LIST* nbl = (NET_BUFFER_LIST*)LayerData;
    NET_BUFFER* nb;
    ULONG direction;
    ULONG eventFlags;
    ULONG skipBytes;
    UINT64 payloadTotal = 0;
    BOOLEAN isDNS;
    HANDLE injectionHandle;
    FWPS_PACKET_INJECTION_STATE injectionState;

    UNREFERENCED_PARAMETER(ClassifyContext);
    UNREFERENCED_PARAMETER(Filter);

    if ((ClassifyOut->rights & FWPS_RIGHT_ACTION_WRITE) == 0 || nbl == NULL) {
        return;
    }

    injectionHandle = Values->layerId == FWPS_LAYER_DATAGRAM_DATA_V4
        ? g_RpState.InjectionHandleV4
        : g_RpState.InjectionHandleV6;
    injectionState = FwpsQueryPacketInjectionState0(injectionHandle, nbl, NULL);
    if (injectionState == FWPS_PACKET_INJECTED_BY_SELF ||
        injectionState == FWPS_PACKET_PREVIOUSLY_INJECTED_BY_SELF) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    if (!RpControllerHealthy()) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        return;
    }

    /*
     * The replayed first UDP packet can reach DATAGRAM_DATA before
     * ALE_FLOW_ESTABLISHED has attached a flow context. Resolve it by tuple in
     * that narrow window; a ProcessId of zero is intentionally a wildcard in
     * RpKeysEqual because injected packets may not carry process metadata.
     */
    if (flow == NULL) {
        if (RpExtractDatagramKey(Values, Metadata, &fallbackKey)) {
            flow = RpFindFlowByKey(&fallbackKey);
            referencedFlow = flow != NULL;
        }
    }
    if (flow == NULL || flow->Removed) {
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        goto Exit;
    }

    direction = RpDatagramDirection(Values);
    eventFlags = direction == FWP_DIRECTION_OUTBOUND ? RP_WFP_EVENT_FLAG_OUTBOUND : 0;
    if (direction == FWP_DIRECTION_OUTBOUND) {
        skipBytes = FWPS_IS_METADATA_FIELD_PRESENT(
            Metadata,
            FWPS_METADATA_FIELD_TRANSPORT_HEADER_SIZE)
            ? Metadata->transportHeaderSize
            : RP_UDP_HEADER_SIZE;
    } else {
        skipBytes = 0;
    }
    isDNS = flow->Key.DestinationPort == 53;

    RpTouchFlow(flow);
    RpRememberDatagramInterface(Values, Metadata, flow);

    if (flow->Action == RP_WFP_ACTION_REJECT) {
        ClassifyOut->actionType = FWP_ACTION_BLOCK;
        ClassifyOut->flags |= FWPS_CLASSIFY_OUT_FLAG_ABSORB;
        ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
        goto Exit;
    }

    for (nb = NET_BUFFER_LIST_FIRST_NB(nbl); nb != NULL; nb = NET_BUFFER_NEXT_NB(nb)) {
        ULONG length = NET_BUFFER_DATA_LENGTH(nb);
        ULONG payloadLength = length > skipBytes ? length - skipBytes : 0;
        payloadTotal += payloadLength;

        if (flow->Action == RP_WFP_ACTION_PROXY && direction == FWP_DIRECTION_OUTBOUND) {
            (VOID)RpQueueNetBufferPayload(
                RP_WFP_EVENT_UDP_DATA,
                flow,
                eventFlags,
                nb,
                skipBytes);
        } else if (flow->Action == RP_WFP_ACTION_DIRECT &&
                   isDNS &&
                   payloadLength != 0) {
            (VOID)RpQueueNetBufferPayload(
                RP_WFP_EVENT_DNS,
                flow,
                eventFlags,
                nb,
                skipBytes);
        }
    }

    if (flow->Action == RP_WFP_ACTION_DIRECT) {
        if (direction == FWP_DIRECTION_OUTBOUND) {
            InterlockedAdd64(&flow->UploadBytes, (LONG64)payloadTotal);
        } else {
            InterlockedAdd64(&flow->DownloadBytes, (LONG64)payloadTotal);
        }
        ClassifyOut->actionType = FWP_ACTION_PERMIT;
        goto Exit;
    }

    if (flow->Action == RP_WFP_ACTION_PROXY &&
        direction == FWP_DIRECTION_OUTBOUND) {
        ClassifyOut->actionType = FWP_ACTION_BLOCK;
        ClassifyOut->flags |= FWPS_CLASSIFY_OUT_FLAG_ABSORB;
        ClassifyOut->rights &= ~FWPS_RIGHT_ACTION_WRITE;
        goto Exit;
    }

    ClassifyOut->actionType = FWP_ACTION_PERMIT;

Exit:
    if (referencedFlow && flow != NULL) {
        RpDereferenceFlow(flow);
    }
}

static VOID NTAPI RpStreamClassify(
    _In_ const FWPS_INCOMING_VALUES0* Values,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _Inout_opt_ VOID* LayerData,
    _In_opt_ const VOID* ClassifyContext,
    _In_ const FWPS_FILTER1* Filter,
    _In_ UINT64 FlowContext,
    _Inout_ FWPS_CLASSIFY_OUT0* ClassifyOut)
{
    RP_FLOW* flow = (RP_FLOW*)(ULONG_PTR)FlowContext;
    FWPS_STREAM_CALLOUT_IO_PACKET0* ioPacket = (FWPS_STREAM_CALLOUT_IO_PACKET0*)LayerData;
    ULONG direction;

    UNREFERENCED_PARAMETER(Metadata);
    UNREFERENCED_PARAMETER(ClassifyContext);
    UNREFERENCED_PARAMETER(Filter);

    if (ioPacket != NULL) {
        ioPacket->streamAction = FWPS_STREAM_ACTION_NONE;
    }
    ClassifyOut->actionType = FWP_ACTION_PERMIT;

    if (flow == NULL ||
        flow->Removed ||
        flow->Action != RP_WFP_ACTION_DIRECT ||
        ioPacket == NULL ||
        ioPacket->streamData == NULL) {
        return;
    }

    direction = Values->layerId == FWPS_LAYER_STREAM_V4
        ? Values->incomingValue[FWPS_FIELD_STREAM_V4_DIRECTION].value.uint32
        : Values->incomingValue[FWPS_FIELD_STREAM_V6_DIRECTION].value.uint32;

    if (direction == FWP_DIRECTION_OUTBOUND) {
        InterlockedAdd64(&flow->UploadBytes, (LONG64)ioPacket->streamData->dataLength);
    } else {
        InterlockedAdd64(&flow->DownloadBytes, (LONG64)ioPacket->streamData->dataLength);
    }
    RpTouchFlow(flow);
}

static NTSTATUS RpRegisterRuntimeCallout(
    _In_ PDEVICE_OBJECT DeviceObject,
    _In_ const GUID* Key,
    _In_ FWPS_CALLOUT_CLASSIFY_FN1 ClassifyFn,
    _In_opt_ FWPS_CALLOUT_FLOW_DELETE_NOTIFY_FN0 FlowDeleteFn,
    _Out_ UINT32* CalloutId)
{
    FWPS_CALLOUT1 callout;
    RtlZeroMemory(&callout, sizeof(callout));
    callout.calloutKey = *Key;
    callout.classifyFn = ClassifyFn;
    callout.notifyFn = RpNotify;
    callout.flowDeleteFn = FlowDeleteFn;
    return FwpsCalloutRegister1(DeviceObject, &callout, CalloutId);
}

static NTSTATUS RpAddCalloutAndFilter(
    _In_ HANDLE Engine,
    _In_ const GUID* CalloutKey,
    _In_ const GUID* LayerKey,
    _In_ const wchar_t* Name,
    _In_ FWP_ACTION_TYPE ActionType)
{
    FWPM_CALLOUT0 callout;
    FWPM_FILTER0 filter;
    NTSTATUS status;

    RtlZeroMemory(&callout, sizeof(callout));
    callout.calloutKey = *CalloutKey;
    callout.displayData.name = (wchar_t*)Name;
    callout.displayData.description = (wchar_t*)Name;
    callout.providerKey = (GUID*)&RP_PROVIDER_GUID;
    callout.applicableLayer = *LayerKey;

    status = FwpmCalloutAdd0(Engine, &callout, NULL, NULL);
    if (!NT_SUCCESS(status)) {
        return status;
    }

    RtlZeroMemory(&filter, sizeof(filter));
    filter.displayData.name = (wchar_t*)Name;
    filter.displayData.description = (wchar_t*)Name;
    filter.providerKey = (GUID*)&RP_PROVIDER_GUID;
    filter.layerKey = *LayerKey;
    filter.subLayerKey = RP_SUBLAYER_GUID;
    filter.weight.type = FWP_EMPTY;
    filter.action.type = ActionType;
    filter.action.calloutKey = *CalloutKey;
    return FwpmFilterAdd0(Engine, &filter, NULL, NULL);
}

static VOID RpUnregisterRuntimeCallouts(VOID)
{
    UINT32* ids[] = {
        &g_RpState.AuthV4Id, &g_RpState.AuthV6Id,
        &g_RpState.RedirectV4Id, &g_RpState.RedirectV6Id,
        &g_RpState.DatagramV4Id, &g_RpState.DatagramV6Id,
        &g_RpState.FlowV4Id, &g_RpState.FlowV6Id,
        &g_RpState.StreamV4Id, &g_RpState.StreamV6Id
    };
    ULONG index;
    for (index = 0; index < RTL_NUMBER_OF(ids); index++) {
        if (*ids[index] != 0) {
            (VOID)FwpsCalloutUnregisterById0(*ids[index]);
            *ids[index] = 0;
        }
    }
}

NTSTATUS RpWfpStart(_In_ PDEVICE_OBJECT DeviceObject)
{
    FWPM_SESSION0 session;
    FWPM_PROVIDER0 provider;
    FWPM_SUBLAYER0 subLayer;
    NET_BUFFER_LIST_POOL_PARAMETERS poolParameters;
    NTSTATUS status;

    RtlZeroMemory(&session, sizeof(session));
    session.flags = FWPM_SESSION_FLAG_DYNAMIC;
    session.displayData.name = L"RelayProxy WFP dynamic session";

    status = FwpmEngineOpen0(
        NULL,
        RPC_C_AUTHN_WINNT,
        NULL,
        &session,
        &g_RpState.EngineHandle);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    status = FwpsRedirectHandleCreate0(
        &RP_PROVIDER_GUID,
        0,
        &g_RpState.RedirectHandle);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    status = FwpsInjectionHandleCreate0(
        AF_INET,
        FWPS_INJECTION_TYPE_TRANSPORT,
        &g_RpState.InjectionHandleV4);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }
    status = FwpsInjectionHandleCreate0(
        AF_INET6,
        FWPS_INJECTION_TYPE_TRANSPORT,
        &g_RpState.InjectionHandleV6);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    /*
     * Initial outbound UDP replay deliberately uses separate handles. The
     * DATAGRAM callout must see those packets again and apply the chosen
     * DIRECT/PROXY decision, while receive-side reply injection is bypassed as
     * self-injected traffic.
     */
    status = FwpsInjectionHandleCreate0(
        AF_INET,
        FWPS_INJECTION_TYPE_TRANSPORT,
        &g_RpState.ReplayInjectionHandleV4);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }
    status = FwpsInjectionHandleCreate0(
        AF_INET6,
        FWPS_INJECTION_TYPE_TRANSPORT,
        &g_RpState.ReplayInjectionHandleV6);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    RtlZeroMemory(&poolParameters, sizeof(poolParameters));
    poolParameters.Header.Type = NDIS_OBJECT_TYPE_DEFAULT;
    poolParameters.Header.Revision = NET_BUFFER_LIST_POOL_PARAMETERS_REVISION_1;
    poolParameters.Header.Size = NDIS_SIZEOF_NET_BUFFER_LIST_POOL_PARAMETERS_REVISION_1;
    poolParameters.ProtocolId = NDIS_PROTOCOL_ID_DEFAULT;
    poolParameters.fAllocateNetBuffer = TRUE;
    poolParameters.PoolTag = RP_TAG_UDP;
    g_RpState.NblPool = NdisAllocateNetBufferListPool(NULL, &poolParameters);
    if (g_RpState.NblPool == NULL) {
        status = STATUS_INSUFFICIENT_RESOURCES;
        goto Exit;
    }

#define RP_REGISTER_RUNTIME(KEY, FN, DELETE_FN, ID) \
    status = RpRegisterRuntimeCallout(DeviceObject, &(KEY), (FN), (DELETE_FN), &(ID)); \
    if (!NT_SUCCESS(status)) goto Exit

    RP_REGISTER_RUNTIME(RP_CALLOUT_AUTH_V4, RpAuthClassify, NULL, g_RpState.AuthV4Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_AUTH_V6, RpAuthClassify, NULL, g_RpState.AuthV6Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_REDIRECT_V4, RpRedirectClassify, NULL, g_RpState.RedirectV4Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_REDIRECT_V6, RpRedirectClassify, NULL, g_RpState.RedirectV6Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_DATAGRAM_V4, RpDatagramClassify, RpFlowDelete, g_RpState.DatagramV4Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_DATAGRAM_V6, RpDatagramClassify, RpFlowDelete, g_RpState.DatagramV6Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_FLOW_V4, RpFlowEstablishedClassify, NULL, g_RpState.FlowV4Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_FLOW_V6, RpFlowEstablishedClassify, NULL, g_RpState.FlowV6Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_STREAM_V4, RpStreamClassify, RpFlowDelete, g_RpState.StreamV4Id);
    RP_REGISTER_RUNTIME(RP_CALLOUT_STREAM_V6, RpStreamClassify, RpFlowDelete, g_RpState.StreamV6Id);

#undef RP_REGISTER_RUNTIME

    RtlZeroMemory(&provider, sizeof(provider));
    provider.providerKey = RP_PROVIDER_GUID;
    provider.displayData.name = L"RelayProxy";
    provider.displayData.description = L"RelayProxy native transparent proxy";
    status = FwpmProviderAdd0(g_RpState.EngineHandle, &provider, NULL);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    RtlZeroMemory(&subLayer, sizeof(subLayer));
    subLayer.subLayerKey = RP_SUBLAYER_GUID;
    subLayer.displayData.name = L"RelayProxy";
    subLayer.displayData.description = L"RelayProxy transparent proxy filters";
    subLayer.providerKey = (GUID*)&RP_PROVIDER_GUID;
    subLayer.weight = 0x7f00;
    status = FwpmSubLayerAdd0(g_RpState.EngineHandle, &subLayer, NULL);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

#define RP_ADD_FILTER(KEY, LAYER, NAME, ACTION) \
    status = RpAddCalloutAndFilter(g_RpState.EngineHandle, &(KEY), &(LAYER), (NAME), (ACTION)); \
    if (!NT_SUCCESS(status)) goto Exit

    RP_ADD_FILTER(RP_CALLOUT_AUTH_V4, FWPM_LAYER_ALE_AUTH_CONNECT_V4, L"RelayProxy ALE auth IPv4", FWP_ACTION_CALLOUT_TERMINATING);
    RP_ADD_FILTER(RP_CALLOUT_AUTH_V6, FWPM_LAYER_ALE_AUTH_CONNECT_V6, L"RelayProxy ALE auth IPv6", FWP_ACTION_CALLOUT_TERMINATING);
    RP_ADD_FILTER(RP_CALLOUT_REDIRECT_V4, FWPM_LAYER_ALE_CONNECT_REDIRECT_V4, L"RelayProxy TCP redirect IPv4", FWP_ACTION_CALLOUT_TERMINATING);
    RP_ADD_FILTER(RP_CALLOUT_REDIRECT_V6, FWPM_LAYER_ALE_CONNECT_REDIRECT_V6, L"RelayProxy TCP redirect IPv6", FWP_ACTION_CALLOUT_TERMINATING);
    RP_ADD_FILTER(RP_CALLOUT_DATAGRAM_V4, FWPM_LAYER_DATAGRAM_DATA_V4, L"RelayProxy UDP datagram IPv4", FWP_ACTION_CALLOUT_TERMINATING);
    RP_ADD_FILTER(RP_CALLOUT_DATAGRAM_V6, FWPM_LAYER_DATAGRAM_DATA_V6, L"RelayProxy UDP datagram IPv6", FWP_ACTION_CALLOUT_TERMINATING);
    RP_ADD_FILTER(RP_CALLOUT_FLOW_V4, FWPM_LAYER_ALE_FLOW_ESTABLISHED_V4, L"RelayProxy flow association IPv4", FWP_ACTION_CALLOUT_INSPECTION);
    RP_ADD_FILTER(RP_CALLOUT_FLOW_V6, FWPM_LAYER_ALE_FLOW_ESTABLISHED_V6, L"RelayProxy flow association IPv6", FWP_ACTION_CALLOUT_INSPECTION);
    RP_ADD_FILTER(RP_CALLOUT_STREAM_V4, FWPM_LAYER_STREAM_V4, L"RelayProxy TCP accounting IPv4", FWP_ACTION_CALLOUT_INSPECTION);
    RP_ADD_FILTER(RP_CALLOUT_STREAM_V6, FWPM_LAYER_STREAM_V6, L"RelayProxy TCP accounting IPv6", FWP_ACTION_CALLOUT_INSPECTION);

#undef RP_ADD_FILTER

    return STATUS_SUCCESS;

Exit:
    RpWfpStop();
    return status;
}

VOID RpWfpStop(VOID)
{
    if (g_RpState.EngineHandle != NULL) {
        FwpmEngineClose0(g_RpState.EngineHandle);
        g_RpState.EngineHandle = NULL;
    }

    RpUnregisterRuntimeCallouts();

    if (g_RpState.RedirectHandle != NULL) {
        FwpsRedirectHandleDestroy0(g_RpState.RedirectHandle);
        g_RpState.RedirectHandle = NULL;
    }
    if (g_RpState.InjectionHandleV4 != NULL) {
        FwpsInjectionHandleDestroy0(g_RpState.InjectionHandleV4);
        g_RpState.InjectionHandleV4 = NULL;
    }
    if (g_RpState.InjectionHandleV6 != NULL) {
        FwpsInjectionHandleDestroy0(g_RpState.InjectionHandleV6);
        g_RpState.InjectionHandleV6 = NULL;
    }
    if (g_RpState.ReplayInjectionHandleV4 != NULL) {
        FwpsInjectionHandleDestroy0(g_RpState.ReplayInjectionHandleV4);
        g_RpState.ReplayInjectionHandleV4 = NULL;
    }
    if (g_RpState.ReplayInjectionHandleV6 != NULL) {
        FwpsInjectionHandleDestroy0(g_RpState.ReplayInjectionHandleV6);
        g_RpState.ReplayInjectionHandleV6 = NULL;
    }
    if (g_RpState.NblPool != NULL) {
        NdisFreeNetBufferListPool(g_RpState.NblPool);
        g_RpState.NblPool = NULL;
    }
}
