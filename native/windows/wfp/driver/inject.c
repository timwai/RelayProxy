#include "driver.h"

typedef struct _RP_UDP_INJECTION_CONTEXT {
    PUCHAR Buffer;
    PMDL Mdl;
} RP_UDP_INJECTION_CONTEXT;

typedef struct _RP_UDP_REPLAY_CONTEXT {
    RP_FLOW* Flow;
    UCHAR RemoteAddress[16];
    UCHAR* ControlData;
    ULONG ControlDataLength;
} RP_UDP_REPLAY_CONTEXT;

static VOID NTAPI RpUdpReplayComplete(
    _Inout_ VOID* Context,
    _Inout_ NET_BUFFER_LIST* NetBufferList,
    _In_ BOOLEAN DispatchLevel)
{
    RP_UDP_REPLAY_CONTEXT* replay = (RP_UDP_REPLAY_CONTEXT*)Context;
    UNREFERENCED_PARAMETER(DispatchLevel);

    if (NetBufferList != NULL) {
        FwpsFreeCloneNetBufferList(NetBufferList, 0);
    }
    if (replay != NULL) {
        if (replay->Flow != NULL) {
            RpDereferenceFlow(replay->Flow);
        }
        if (replay->ControlData != NULL) {
            ExFreePoolWithTag(replay->ControlData, RP_TAG_REPLAY);
        }
        ExFreePoolWithTag(replay, RP_TAG_REPLAY);
    }
}

NTSTATUS RpCapturePendingUdp(
    _Inout_ RP_FLOW* Flow,
    _In_ const FWPS_INCOMING_METADATA_VALUES0* Metadata,
    _In_ NET_BUFFER_LIST* NetBufferList)
{
    UCHAR* controlData = NULL;
    ULONG controlDataLength = 0;
    KIRQL oldIrql;

    if (Flow == NULL || Metadata == NULL || NetBufferList == NULL ||
        Flow->Key.Protocol != IPPROTO_UDP) {
        return STATUS_INVALID_PARAMETER;
    }
    if (!FWPS_IS_METADATA_FIELD_PRESENT(
            Metadata,
            FWPS_METADATA_FIELD_TRANSPORT_ENDPOINT_HANDLE)) {
        return STATUS_NOT_SUPPORTED;
    }

    if (FWPS_IS_METADATA_FIELD_PRESENT(
            Metadata,
            FWPS_METADATA_FIELD_TRANSPORT_CONTROL_DATA) &&
        Metadata->controlData != NULL &&
        Metadata->controlDataLength != 0) {
        controlDataLength = Metadata->controlDataLength;
        if (controlDataLength > 64u * 1024u) {
            return STATUS_INVALID_BUFFER_SIZE;
        }
        controlData = (UCHAR*)ExAllocatePool2(
            POOL_FLAG_NON_PAGED,
            controlDataLength,
            RP_TAG_REPLAY);
        if (controlData == NULL) {
            return STATUS_INSUFFICIENT_RESOURCES;
        }
        RtlCopyMemory(controlData, Metadata->controlData, controlDataLength);
    }

    FwpsReferenceNetBufferList(NetBufferList, TRUE);

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (Flow->Removed || Flow->PendingUdpNbl != NULL) {
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        FwpsDereferenceNetBufferList(NetBufferList, TRUE);
        if (controlData != NULL) {
            ExFreePoolWithTag(controlData, RP_TAG_REPLAY);
        }
        return STATUS_INVALID_DEVICE_STATE;
    }

    Flow->PendingUdpNbl = NetBufferList;
    Flow->PendingUdpEndpointHandle = Metadata->transportEndpointHandle;
    Flow->PendingUdpRemoteScopeId = Metadata->remoteScopeId;
    Flow->PendingUdpControlData = controlData;
    Flow->PendingUdpControlDataLength = controlDataLength;
    Flow->PendingUdpReplay = 0;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return STATUS_SUCCESS;
}

VOID RpReleasePendingUdp(_Inout_ RP_FLOW* Flow)
{
    NET_BUFFER_LIST* nbl = NULL;
    UCHAR* controlData = NULL;
    KIRQL oldIrql;

    if (Flow == NULL) {
        return;
    }

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    nbl = Flow->PendingUdpNbl;
    controlData = Flow->PendingUdpControlData;
    Flow->PendingUdpNbl = NULL;
    Flow->PendingUdpControlData = NULL;
    Flow->PendingUdpControlDataLength = 0;
    Flow->PendingUdpEndpointHandle = 0;
    Flow->PendingUdpReplay = 1;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    if (nbl != NULL) {
        FwpsDereferenceNetBufferList(nbl, TRUE);
    }
    if (controlData != NULL) {
        ExFreePoolWithTag(controlData, RP_TAG_REPLAY);
    }
}

NTSTATUS RpReplayPendingUdp(_Inout_ RP_FLOW* Flow)
{
    RP_UDP_REPLAY_CONTEXT* replay = NULL;
    NET_BUFFER_LIST* original = NULL;
    NET_BUFFER_LIST* clone = NULL;
    FWPS_TRANSPORT_SEND_PARAMS0 sendParams;
    UCHAR* controlData = NULL;
    ULONG controlDataLength = 0;
    UINT64 endpointHandle = 0;
    SCOPE_ID remoteScopeId;
    ADDRESS_FAMILY family;
    HANDLE injectionHandle;
    KIRQL oldIrql;
    NTSTATUS status;

    if (Flow == NULL || Flow->Key.Protocol != IPPROTO_UDP) {
        return STATUS_INVALID_PARAMETER;
    }

    replay = (RP_UDP_REPLAY_CONTEXT*)ExAllocatePool2(
        POOL_FLAG_NON_PAGED,
        sizeof(*replay),
        RP_TAG_REPLAY);
    if (replay == NULL) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    RtlZeroMemory(replay, sizeof(*replay));
    RtlZeroMemory(&remoteScopeId, sizeof(remoteScopeId));

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (Flow->Removed ||
        Flow->PendingUdpNbl == NULL ||
        InterlockedCompareExchange(&Flow->PendingUdpReplay, 1, 0) != 0) {
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        ExFreePoolWithTag(replay, RP_TAG_REPLAY);
        return STATUS_SUCCESS;
    }

    original = Flow->PendingUdpNbl;
    endpointHandle = Flow->PendingUdpEndpointHandle;
    remoteScopeId = Flow->PendingUdpRemoteScopeId;
    controlData = Flow->PendingUdpControlData;
    controlDataLength = Flow->PendingUdpControlDataLength;

    Flow->PendingUdpNbl = NULL;
    Flow->PendingUdpEndpointHandle = 0;
    Flow->PendingUdpControlData = NULL;
    Flow->PendingUdpControlDataLength = 0;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    RpReferenceFlow(Flow);
    replay->Flow = Flow;
    RtlCopyMemory(replay->RemoteAddress, Flow->Key.DestinationAddress, 16);
    replay->ControlData = controlData;
    replay->ControlDataLength = controlDataLength;

    status = FwpsAllocateCloneNetBufferList(
        original,
        NULL,
        NULL,
        0,
        &clone);
    FwpsDereferenceNetBufferList(original, TRUE);
    original = NULL;
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    RtlZeroMemory(&sendParams, sizeof(sendParams));
    sendParams.remoteAddress = replay->RemoteAddress;
    sendParams.remoteScopeId = remoteScopeId;
    sendParams.controlData = (WSACMSGHDR*)replay->ControlData;
    sendParams.controlDataLength = replay->ControlDataLength;

    if (Flow->Key.Family == 4) {
        family = AF_INET;
        injectionHandle = g_RpState.ReplayInjectionHandleV4;
    } else if (Flow->Key.Family == 6) {
        family = AF_INET6;
        injectionHandle = g_RpState.ReplayInjectionHandleV6;
    } else {
        status = STATUS_INVALID_ADDRESS;
        goto Exit;
    }

    status = FwpsInjectTransportSendAsync0(
        injectionHandle,
        Flow,
        endpointHandle,
        0,
        &sendParams,
        family,
        Flow->CompartmentId,
        clone,
        RpUdpReplayComplete,
        replay);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    clone = NULL;
    replay = NULL;
    return STATUS_SUCCESS;

Exit:
    if (clone != NULL) {
        FwpsFreeCloneNetBufferList(clone, 0);
    }
    if (original != NULL) {
        FwpsDereferenceNetBufferList(original, TRUE);
    }
    if (replay != NULL) {
        if (replay->Flow != NULL) {
            RpDereferenceFlow(replay->Flow);
        }
        if (replay->ControlData != NULL) {
            ExFreePoolWithTag(replay->ControlData, RP_TAG_REPLAY);
        }
        ExFreePoolWithTag(replay, RP_TAG_REPLAY);
    }
    return status;
}

static VOID RpWriteBE16(_Out_writes_(2) UCHAR* Dst, _In_ UINT16 Value)
{
    Dst[0] = (UCHAR)(Value >> 8);
    Dst[1] = (UCHAR)(Value & 0xff);
}

static UINT16 RpFoldChecksum(_In_ UINT32 Sum)
{
    while ((Sum >> 16) != 0) {
        Sum = (Sum & 0xffffu) + (Sum >> 16);
    }
    return (UINT16)~Sum;
}

static UINT32 RpChecksumBytes(_In_reads_bytes_(Length) const UCHAR* Data, _In_ ULONG Length, _In_ UINT32 Sum)
{
    ULONG index = 0;
    while (index + 1 < Length) {
        Sum += ((UINT32)Data[index] << 8) | Data[index + 1];
        index += 2;
    }
    if (index < Length) {
        Sum += (UINT32)Data[index] << 8;
    }
    return Sum;
}

static UINT16 RpIPv4HeaderChecksum(_In_reads_bytes_(20) const UCHAR* Header)
{
    return RpFoldChecksum(RpChecksumBytes(Header, 20, 0));
}

static UINT16 RpIPv6UDPChecksum(
    _In_reads_(16) const UCHAR Source[16],
    _In_reads_(16) const UCHAR Destination[16],
    _In_reads_bytes_(UdpLength) const UCHAR* Udp,
    _In_ ULONG UdpLength)
{
    UINT32 sum = 0;
    UINT16 result;

    sum = RpChecksumBytes(Source, 16, sum);
    sum = RpChecksumBytes(Destination, 16, sum);
    sum += (UdpLength >> 16) & 0xffffu;
    sum += UdpLength & 0xffffu;
    sum += 17u; /* IPPROTO_UDP */
    sum = RpChecksumBytes(Udp, UdpLength, sum);

    result = RpFoldChecksum(sum);
    return result == 0 ? 0xffffu : result;
}

static VOID NTAPI RpUdpInjectComplete(
    _Inout_ VOID* Context,
    _Inout_ NET_BUFFER_LIST* NetBufferList,
    _In_ BOOLEAN DispatchLevel)
{
    RP_UDP_INJECTION_CONTEXT* injection = (RP_UDP_INJECTION_CONTEXT*)Context;
    UNREFERENCED_PARAMETER(DispatchLevel);

    if (NetBufferList != NULL) {
        FwpsFreeNetBufferList(NetBufferList);
    }
    if (injection != NULL) {
        if (injection->Mdl != NULL) {
            IoFreeMdl(injection->Mdl);
        }
        if (injection->Buffer != NULL) {
            ExFreePoolWithTag(injection->Buffer, RP_TAG_UDP);
        }
        ExFreePoolWithTag(injection, RP_TAG_UDP);
    }
}

static NTSTATUS RpBuildUdpPacket(
    _In_ const RP_FLOW* Flow,
    _In_reads_bytes_(PayloadLength) const UCHAR* Payload,
    _In_ ULONG PayloadLength,
    _Outptr_ RP_UDP_INJECTION_CONTEXT** Context,
    _Outptr_ NET_BUFFER_LIST** NetBufferList)
{
    ULONG ipHeaderLength;
    ULONG udpLength;
    ULONG packetLength;
    RP_UDP_INJECTION_CONTEXT* injection = NULL;
    NET_BUFFER_LIST* nbl = NULL;
    UCHAR* udp;
    NTSTATUS status;

    if (Flow == NULL || Payload == NULL || Context == NULL || NetBufferList == NULL) {
        return STATUS_INVALID_PARAMETER;
    }

    *Context = NULL;
    *NetBufferList = NULL;

    ipHeaderLength = Flow->Key.Family == 4 ? 20u : 40u;
    udpLength = 8u + PayloadLength;
    packetLength = ipHeaderLength + udpLength;
    if (packetLength > 65535u || udpLength > 65535u) {
        return STATUS_INVALID_BUFFER_SIZE;
    }

    injection = (RP_UDP_INJECTION_CONTEXT*)ExAllocatePool2(
        POOL_FLAG_NON_PAGED,
        sizeof(*injection),
        RP_TAG_UDP);
    if (injection == NULL) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    RtlZeroMemory(injection, sizeof(*injection));

    injection->Buffer = (PUCHAR)ExAllocatePool2(
        POOL_FLAG_NON_PAGED,
        packetLength,
        RP_TAG_UDP);
    if (injection->Buffer == NULL) {
        status = STATUS_INSUFFICIENT_RESOURCES;
        goto Exit;
    }
    RtlZeroMemory(injection->Buffer, packetLength);

    if (Flow->Key.Family == 4) {
        UCHAR* ip = injection->Buffer;
        UINT16 checksum;
        ip[0] = 0x45;
        RpWriteBE16(ip + 2, (UINT16)packetLength);
        ip[8] = 64;
        ip[9] = 17;
        RtlCopyMemory(ip + 12, Flow->Key.DestinationAddress, 4);
        RtlCopyMemory(ip + 16, Flow->Key.SourceAddress, 4);
        checksum = RpIPv4HeaderChecksum(ip);
        RpWriteBE16(ip + 10, checksum);
    } else if (Flow->Key.Family == 6) {
        UCHAR* ip = injection->Buffer;
        ip[0] = 0x60;
        RpWriteBE16(ip + 4, (UINT16)udpLength);
        ip[6] = 17;
        ip[7] = 64;
        RtlCopyMemory(ip + 8, Flow->Key.DestinationAddress, 16);
        RtlCopyMemory(ip + 24, Flow->Key.SourceAddress, 16);
    } else {
        status = STATUS_INVALID_ADDRESS;
        goto Exit;
    }

    udp = injection->Buffer + ipHeaderLength;
    RpWriteBE16(udp + 0, Flow->Key.DestinationPort);
    RpWriteBE16(udp + 2, Flow->Key.SourcePort);
    RpWriteBE16(udp + 4, (UINT16)udpLength);
    RtlCopyMemory(udp + 8, Payload, PayloadLength);

    if (Flow->Key.Family == 6) {
        UINT16 checksum = RpIPv6UDPChecksum(
            Flow->Key.DestinationAddress,
            Flow->Key.SourceAddress,
            udp,
            udpLength);
        RpWriteBE16(udp + 6, checksum);
    }

    injection->Mdl = IoAllocateMdl(
        injection->Buffer,
        packetLength,
        FALSE,
        FALSE,
        NULL);
    if (injection->Mdl == NULL) {
        status = STATUS_INSUFFICIENT_RESOURCES;
        goto Exit;
    }
    MmBuildMdlForNonPagedPool(injection->Mdl);

    status = FwpsAllocateNetBufferAndNetBufferList(
        g_RpState.NblPool,
        0,
        0,
        injection->Mdl,
        0,
        packetLength,
        &nbl);
    if (!NT_SUCCESS(status)) {
        goto Exit;
    }

    *Context = injection;
    *NetBufferList = nbl;
    return STATUS_SUCCESS;

Exit:
    if (nbl != NULL) {
        FwpsFreeNetBufferList(nbl);
    }
    if (injection != NULL) {
        if (injection->Mdl != NULL) {
            IoFreeMdl(injection->Mdl);
        }
        if (injection->Buffer != NULL) {
            ExFreePoolWithTag(injection->Buffer, RP_TAG_UDP);
        }
        ExFreePoolWithTag(injection, RP_TAG_UDP);
    }
    return status;
}

NTSTATUS RpInjectUdp(_In_ const RP_WFP_UDP_INJECT* Request, _In_ ULONG InputLength)
{
    RP_FLOW* flow;
    RP_UDP_INJECTION_CONTEXT* context = NULL;
    NET_BUFFER_LIST* nbl = NULL;
    HANDLE injectionHandle;
    ADDRESS_FAMILY family;
    NTSTATUS status;
    ULONG expectedSize;

    if (Request == NULL ||
        InputLength < (ULONG)FIELD_OFFSET(RP_WFP_UDP_INJECT, Payload) ||
        Request->AbiVersion != RP_WFP_ABI_VERSION ||
        Request->AssociationId == 0 ||
        Request->PayloadLength > RP_WFP_MAX_UDP_PAYLOAD) {
        return STATUS_INVALID_PARAMETER;
    }

    expectedSize = (ULONG)FIELD_OFFSET(RP_WFP_UDP_INJECT, Payload) + Request->PayloadLength;
    if (Request->Size != expectedSize || InputLength != expectedSize) {
        return STATUS_INVALID_BUFFER_SIZE;
    }

    flow = RpFindFlowByAssociationId(Request->AssociationId);
    if (flow == NULL) {
        return STATUS_NOT_FOUND;
    }

    if (flow->Removed ||
        flow->Key.Protocol != 17 ||
        flow->Action != RP_WFP_ACTION_PROXY ||
        flow->InterfaceIndex == 0) {
        RpDereferenceFlow(flow);
        return STATUS_INVALID_DEVICE_STATE;
    }

    status = RpBuildUdpPacket(
        flow,
        Request->Payload,
        Request->PayloadLength,
        &context,
        &nbl);
    if (!NT_SUCCESS(status)) {
        RpDereferenceFlow(flow);
        return status;
    }

    if (flow->Key.Family == 4) {
        family = AF_INET;
        injectionHandle = g_RpState.InjectionHandleV4;
    } else {
        family = AF_INET6;
        injectionHandle = g_RpState.InjectionHandleV6;
    }

    status = FwpsInjectTransportReceiveAsync(
        injectionHandle,
        NULL,
        NULL,
        0,
        family,
        flow->CompartmentId,
        flow->InterfaceIndex,
        flow->SubInterfaceIndex,
        nbl,
        RpUdpInjectComplete,
        context);

    if (!NT_SUCCESS(status)) {
        RpUdpInjectComplete(context, nbl, FALSE);
    }

    RpDereferenceFlow(flow);
    return status;
}
