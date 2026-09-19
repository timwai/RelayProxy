#include "driver.h"

typedef struct _RP_UDP_INJECTION_CONTEXT {
    PUCHAR Buffer;
    PMDL Mdl;
} RP_UDP_INJECTION_CONTEXT;

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
