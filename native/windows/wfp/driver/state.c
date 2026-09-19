#include "driver.h"

RP_DRIVER_STATE g_RpState = {0};

static BOOLEAN RpKeysEqual(_In_ const RP_FLOW_KEY* A, _In_ const RP_FLOW_KEY* B)
{
    if (A->Protocol != B->Protocol ||
        A->Family != B->Family ||
        A->SourcePort != B->SourcePort ||
        A->DestinationPort != B->DestinationPort ||
        RtlCompareMemory(A->SourceAddress, B->SourceAddress, sizeof(A->SourceAddress)) != sizeof(A->SourceAddress) ||
        RtlCompareMemory(A->DestinationAddress, B->DestinationAddress, sizeof(A->DestinationAddress)) != sizeof(A->DestinationAddress)) {
        return FALSE;
    }
    return A->ProcessId == 0 || B->ProcessId == 0 || A->ProcessId == B->ProcessId;
}

static VOID RpWatchdogThread(_In_opt_ PVOID Context)
{
    LARGE_INTEGER interval;
    UNREFERENCED_PARAMETER(Context);

    interval.QuadPart = -10000000ll; /* one second, relative */
    for (;;) {
        NTSTATUS status = KeWaitForSingleObject(
            &g_RpState.WatchdogStopEvent,
            Executive,
            KernelMode,
            FALSE,
            &interval);
        if (status == STATUS_SUCCESS) {
            break;
        }
        if (status == STATUS_TIMEOUT) {
            (VOID)RpControllerHealthy();
            continue;
        }
        break;
    }
    PsTerminateSystemThread(STATUS_SUCCESS);
}

UINT64 RpNextId(VOID)
{
    return (UINT64)InterlockedIncrement64(&g_RpState.NextId);
}

VOID RpReferenceFlow(_In_ RP_FLOW* Flow)
{
    InterlockedIncrement(&Flow->RefCount);
}

VOID RpDereferenceFlow(_In_ RP_FLOW* Flow)
{
    if (InterlockedDecrement(&Flow->RefCount) == 0) {
        ExFreePoolWithTag(Flow, RP_TAG_FLOW);
    }
}

NTSTATUS RpStateInitialize(VOID)
{
    NTSTATUS status;

    RtlZeroMemory(&g_RpState, sizeof(g_RpState));
    KeInitializeSpinLock(&g_RpState.Lock);
    InitializeListHead(&g_RpState.Flows);
    InitializeListHead(&g_RpState.Events);
    KeInitializeEvent(&g_RpState.WatchdogStopEvent, NotificationEvent, FALSE);

    status = PsCreateSystemThread(
        &g_RpState.WatchdogThread,
        THREAD_ALL_ACCESS,
        NULL,
        NULL,
        NULL,
        RpWatchdogThread,
        NULL);
    if (!NT_SUCCESS(status)) {
        g_RpState.WatchdogThread = NULL;
        return status;
    }
    return STATUS_SUCCESS;
}

static VOID RpFreeEvents(VOID)
{
    for (;;) {
        RP_EVENT_NODE* node = NULL;
        KIRQL oldIrql;
        KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
        if (!IsListEmpty(&g_RpState.Events)) {
            PLIST_ENTRY entry = RemoveHeadList(&g_RpState.Events);
            g_RpState.EventCount--;
            node = CONTAINING_RECORD(entry, RP_EVENT_NODE, Link);
        }
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        if (node == NULL) {
            break;
        }
        ExFreePoolWithTag(node, RP_TAG_EVENT);
    }
}

VOID RpStateShutdown(VOID)
{
    if (g_RpState.WatchdogThread != NULL) {
        KeSetEvent(&g_RpState.WatchdogStopEvent, IO_NO_INCREMENT, FALSE);
        (VOID)ZwWaitForSingleObject(g_RpState.WatchdogThread, FALSE, NULL);
        ZwClose(g_RpState.WatchdogThread);
        g_RpState.WatchdogThread = NULL;
    }

    RpControllerFailOpen();
    RpFreeEvents();

    for (;;) {
        RP_FLOW* flow = NULL;
        KIRQL oldIrql;
        KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
        if (!IsListEmpty(&g_RpState.Flows)) {
            PLIST_ENTRY entry = RemoveHeadList(&g_RpState.Flows);
            flow = CONTAINING_RECORD(entry, RP_FLOW, Link);
            InterlockedExchange(&flow->Removed, 1);
        }
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        if (flow == NULL) {
            break;
        }
        RpDereferenceFlow(flow);
    }
}

BOOLEAN RpControllerHealthy(VOID)
{
    BOOLEAN healthy;
    UINT64 timeout;
    UINT64 last;
    UINT64 now = KeQueryInterruptTime();
    KIRQL oldIrql;

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    timeout = (UINT64)g_RpState.HeartbeatMs * 10000ull;
    last = g_RpState.LastHeartbeat100ns;
    healthy = g_RpState.ControllerActive &&
              g_RpState.ControllerPid != 0 &&
              g_RpState.HeartbeatMs != 0 &&
              now >= last &&
              (now - last) <= timeout;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    if (!healthy) {
        RpControllerFailOpen();
    }
    return healthy;
}

VOID RpHeartbeat(VOID)
{
    KIRQL oldIrql;
    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (g_RpState.ControllerActive) {
        g_RpState.LastHeartbeat100ns = KeQueryInterruptTime();
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
}

NTSTATUS RpConfigureController(_In_ PIRP Irp, _In_ const RP_WFP_CONFIG* Config)
{
    ULONG requestorPid;
    PIO_STACK_LOCATION stack;
    PFILE_OBJECT fileObject;

    if (Config == NULL ||
        Config->AbiVersion != RP_WFP_ABI_VERSION ||
        Config->Size != sizeof(RP_WFP_CONFIG) ||
        Config->ControllerPid == 0 ||
        Config->TcpPortV4 == 0 ||
        Config->TcpPortV6 == 0 ||
        Config->HeartbeatMs < 2000 ||
        Config->HeartbeatMs > 60000) {
        return STATUS_INVALID_PARAMETER;
    }

    requestorPid = IoGetRequestorProcessId(Irp);
    if (requestorPid == 0 || requestorPid != Config->ControllerPid) {
        return STATUS_ACCESS_DENIED;
    }
    stack = IoGetCurrentIrpStackLocation(Irp);
    fileObject = stack->FileObject;
    if (fileObject == NULL) {
        return STATUS_INVALID_HANDLE;
    }

    {
        KIRQL oldIrql;
        KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
        if (g_RpState.ControllerActive &&
            g_RpState.ControllerFileObject != fileObject) {
            KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
            return STATUS_DEVICE_BUSY;
        }
        g_RpState.ControllerPid = Config->ControllerPid;
        g_RpState.ControllerFileObject = fileObject;
        g_RpState.ProxyReady = FALSE;
        g_RpState.TcpPortV4 = Config->TcpPortV4;
        g_RpState.TcpPortV6 = Config->TcpPortV6;
        g_RpState.HeartbeatMs = Config->HeartbeatMs;
        g_RpState.LastHeartbeat100ns = KeQueryInterruptTime();
        g_RpState.ControllerActive = TRUE;
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    }
    return STATUS_SUCCESS;
}

BOOLEAN RpIsControllerFile(_In_opt_ PFILE_OBJECT FileObject)
{
    BOOLEAN result;
    KIRQL oldIrql;
    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    result = g_RpState.ControllerActive &&
             FileObject != NULL &&
             g_RpState.ControllerFileObject == FileObject;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return result;
}

VOID RpControllerCleanup(_In_opt_ PFILE_OBJECT FileObject)
{
    if (RpIsControllerFile(FileObject)) {
        RpControllerFailOpen();
    }
}

RP_FLOW* RpFindFlowByRequestId(_In_ UINT64 RequestId)
{
    RP_FLOW* found = NULL;
    KIRQL oldIrql;
    PLIST_ENTRY entry;

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    for (entry = g_RpState.Flows.Flink; entry != &g_RpState.Flows; entry = entry->Flink) {
        RP_FLOW* flow = CONTAINING_RECORD(entry, RP_FLOW, Link);
        if (!flow->Removed && flow->RequestId == RequestId) {
            RpReferenceFlow(flow);
            found = flow;
            break;
        }
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return found;
}

RP_FLOW* RpFindFlowByAssociationId(_In_ UINT64 AssociationId)
{
    RP_FLOW* found = NULL;
    KIRQL oldIrql;
    PLIST_ENTRY entry;

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    for (entry = g_RpState.Flows.Flink; entry != &g_RpState.Flows; entry = entry->Flink) {
        RP_FLOW* flow = CONTAINING_RECORD(entry, RP_FLOW, Link);
        if (!flow->Removed && flow->AssociationId == AssociationId) {
            RpReferenceFlow(flow);
            found = flow;
            break;
        }
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return found;
}

RP_FLOW* RpFindFlowByKey(_In_ const RP_FLOW_KEY* Key)
{
    RP_FLOW* found = NULL;
    KIRQL oldIrql;
    PLIST_ENTRY entry;

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    for (entry = g_RpState.Flows.Flink; entry != &g_RpState.Flows; entry = entry->Flink) {
        RP_FLOW* flow = CONTAINING_RECORD(entry, RP_FLOW, Link);
        if (!flow->Removed && RpKeysEqual(&flow->Key, Key)) {
            RpReferenceFlow(flow);
            found = flow;
            break;
        }
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return found;
}

NTSTATUS RpInsertPendingFlow(_In_ RP_FLOW* Flow)
{
    PLIST_ENTRY entry;
    KIRQL oldIrql;

    if (Flow == NULL) {
        return STATUS_INVALID_PARAMETER;
    }

    Flow->RefCount = 1;
    Flow->Removed = 0;
    Flow->LastSeen100ns = KeQueryInterruptTime();

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    for (entry = g_RpState.Flows.Flink; entry != &g_RpState.Flows; entry = entry->Flink) {
        RP_FLOW* existing = CONTAINING_RECORD(entry, RP_FLOW, Link);
        if (!existing->Removed && RpKeysEqual(&existing->Key, &Flow->Key)) {
            KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
            return STATUS_OBJECT_NAME_COLLISION;
        }
    }
    InsertTailList(&g_RpState.Flows, &Flow->Link);
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return STATUS_SUCCESS;
}

VOID RpTouchFlow(_In_ RP_FLOW* Flow)
{
    if (Flow != NULL) {
        Flow->LastSeen100ns = KeQueryInterruptTime();
    }
}

VOID RpRemoveFlow(_In_ RP_FLOW* Flow, _In_ BOOLEAN QueueClose)
{
    BOOLEAN removed = FALSE;
    HANDLE completionContext = NULL;
    KIRQL oldIrql;

    if (Flow == NULL) {
        return;
    }

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (InterlockedCompareExchange(&Flow->Removed, 1, 0) == 0) {
        RemoveEntryList(&Flow->Link);
        InitializeListHead(&Flow->Link);
        completionContext = Flow->CompletionContext;
        Flow->CompletionContext = NULL;
        Flow->Action = RP_WFP_ACTION_DIRECT;
        removed = TRUE;
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    if (!removed) {
        return;
    }
    if (completionContext != NULL) {
        FwpsCompleteOperation0(completionContext, NULL);
    }
    if (QueueClose) {
        UINT64 counters[2];
        counters[0] = (UINT64)InterlockedCompareExchange64(&Flow->UploadBytes, 0, 0);
        counters[1] = (UINT64)InterlockedCompareExchange64(&Flow->DownloadBytes, 0, 0);
        (VOID)RpQueueDatagramEvent(
            RP_WFP_EVENT_CLOSE,
            Flow,
            0,
            (const UCHAR*)counters,
            sizeof(counters));
    }
    RpDereferenceFlow(Flow);
}

VOID RpControllerFailOpen(VOID)
{
    KIRQL oldIrql;

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    g_RpState.ControllerActive = FALSE;
    g_RpState.ProxyReady = FALSE;
    g_RpState.ControllerPid = 0;
    g_RpState.ControllerFileObject = NULL;
    g_RpState.TcpPortV4 = 0;
    g_RpState.TcpPortV6 = 0;
    g_RpState.HeartbeatMs = 0;
    g_RpState.LastHeartbeat100ns = 0;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    for (;;) {
        RP_FLOW* target = NULL;
        HANDLE context = NULL;
        PLIST_ENTRY entry;

        KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
        for (entry = g_RpState.Flows.Flink; entry != &g_RpState.Flows; entry = entry->Flink) {
            RP_FLOW* flow = CONTAINING_RECORD(entry, RP_FLOW, Link);
            if (!flow->Removed && flow->CompletionContext != NULL) {
                target = flow;
                RpReferenceFlow(target);
                context = flow->CompletionContext;
                flow->CompletionContext = NULL;
                flow->Action = RP_WFP_ACTION_DIRECT;
                break;
            }
        }
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

        if (target == NULL) {
            break;
        }
        FwpsCompleteOperation0(context, NULL);
        RpDereferenceFlow(target);
    }
}

static NTSTATUS RpQueueEventInternal(
    _In_ UINT32 Kind,
    _In_ const RP_FLOW* Flow,
    _In_ ULONG Flags,
    _In_opt_ const FWP_BYTE_BLOB* ProcessPath,
    _In_reads_bytes_opt_(PayloadLength) const UCHAR* Payload,
    _In_ ULONG PayloadLength)
{
    SIZE_T eventSize;
    SIZE_T allocationSize;
    RP_EVENT_NODE* node;
    RP_WFP_EVENT* event;
    USHORT pathChars = 0;
    KIRQL oldIrql;

    if (Flow == NULL || PayloadLength > RP_WFP_MAX_UDP_PAYLOAD) {
        return STATUS_INVALID_PARAMETER;
    }

    eventSize = FIELD_OFFSET(RP_WFP_EVENT, Payload) + PayloadLength;
    allocationSize = FIELD_OFFSET(RP_EVENT_NODE, Data) + eventSize;
    node = (RP_EVENT_NODE*)ExAllocatePool2(POOL_FLAG_NON_PAGED, allocationSize, RP_TAG_EVENT);
    if (node == NULL) {
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    RtlZeroMemory(node, allocationSize);
    node->Size = (ULONG)eventSize;
    event = (RP_WFP_EVENT*)node->Data;

    event->AbiVersion = RP_WFP_ABI_VERSION;
    event->Size = (UINT32)eventSize;
    event->Kind = Kind;
    event->Flags = Flags;
    event->RequestId = Flow->RequestId;
    event->AssociationId = Flow->AssociationId;
    event->ProcessId = Flow->Key.ProcessId;
    event->CompartmentId = Flow->CompartmentId;
    event->Protocol = Flow->Key.Protocol;
    event->Family = Flow->Key.Family;
    if ((Kind == RP_WFP_EVENT_DNS || Kind == RP_WFP_EVENT_UDP_DATA) &&
        (Flags & RP_WFP_EVENT_FLAG_OUTBOUND) == 0) {
        event->SourcePort = Flow->Key.DestinationPort;
        event->DestinationPort = Flow->Key.SourcePort;
        RtlCopyMemory(event->SourceAddress, Flow->Key.DestinationAddress, sizeof(event->SourceAddress));
        RtlCopyMemory(event->DestinationAddress, Flow->Key.SourceAddress, sizeof(event->DestinationAddress));
    } else {
        event->SourcePort = Flow->Key.SourcePort;
        event->DestinationPort = Flow->Key.DestinationPort;
        RtlCopyMemory(event->SourceAddress, Flow->Key.SourceAddress, sizeof(event->SourceAddress));
        RtlCopyMemory(event->DestinationAddress, Flow->Key.DestinationAddress, sizeof(event->DestinationAddress));
    }

    if (ProcessPath != NULL && ProcessPath->data != NULL && ProcessPath->size >= sizeof(WCHAR)) {
        SIZE_T chars = ProcessPath->size / sizeof(WCHAR);
        if (chars > RP_WFP_MAX_PATH_CHARS) {
            chars = RP_WFP_MAX_PATH_CHARS;
        }
        while (chars > 0 && ((const WCHAR*)ProcessPath->data)[chars - 1] == L'\0') {
            chars--;
        }
        pathChars = (USHORT)chars;
        if (pathChars != 0) {
            RtlCopyMemory(event->ProcessPath, ProcessPath->data, pathChars * sizeof(WCHAR));
        }
    }
    event->ProcessPathChars = pathChars;

    if (PayloadLength != 0 && Payload != NULL) {
        event->PayloadLength = PayloadLength;
        RtlCopyMemory(event->Payload, Payload, PayloadLength);
    }

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (g_RpState.EventCount >= RP_MAX_EVENTS) {
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        ExFreePoolWithTag(node, RP_TAG_EVENT);
        return STATUS_INSUFFICIENT_RESOURCES;
    }
    InsertTailList(&g_RpState.Events, &node->Link);
    g_RpState.EventCount++;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return STATUS_SUCCESS;
}

NTSTATUS RpQueueFlowEvent(_In_ const RP_FLOW* Flow, _In_opt_ const FWP_BYTE_BLOB* ProcessPath, _In_ ULONG Flags)
{
    return RpQueueEventInternal(RP_WFP_EVENT_FLOW, Flow, Flags, ProcessPath, NULL, 0);
}

NTSTATUS RpQueueDatagramEvent(
    _In_ UINT32 Kind,
    _In_ const RP_FLOW* Flow,
    _In_ ULONG Flags,
    _In_reads_bytes_opt_(PayloadLength) const UCHAR* Payload,
    _In_ ULONG PayloadLength)
{
    return RpQueueEventInternal(Kind, Flow, Flags, NULL, Payload, PayloadLength);
}

NTSTATUS RpReadEvent(_Out_writes_bytes_(OutputLength) VOID* Output, _In_ ULONG OutputLength, _Out_ ULONG_PTR* BytesWritten)
{
    RP_EVENT_NODE* node = NULL;
    KIRQL oldIrql;

    if (BytesWritten == NULL || Output == NULL) {
        return STATUS_INVALID_PARAMETER;
    }
    *BytesWritten = 0;

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (IsListEmpty(&g_RpState.Events)) {
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        return STATUS_NO_MORE_ENTRIES;
    }

    node = CONTAINING_RECORD(g_RpState.Events.Flink, RP_EVENT_NODE, Link);
    if (OutputLength < node->Size) {
        KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
        return STATUS_BUFFER_TOO_SMALL;
    }
    RemoveEntryList(&node->Link);
    g_RpState.EventCount--;
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    RtlCopyMemory(Output, node->Data, node->Size);
    *BytesWritten = node->Size;
    ExFreePoolWithTag(node, RP_TAG_EVENT);
    return STATUS_SUCCESS;
}

NTSTATUS RpApplyDecision(_In_ const RP_WFP_DECISION* Decision)
{
    RP_FLOW* flow;
    HANDLE context = NULL;
    KIRQL oldIrql;

    if (Decision == NULL ||
        Decision->AbiVersion != RP_WFP_ABI_VERSION ||
        Decision->Size != sizeof(RP_WFP_DECISION) ||
        Decision->RequestId == 0 ||
        (Decision->Action != RP_WFP_ACTION_DIRECT &&
         Decision->Action != RP_WFP_ACTION_PROXY &&
         Decision->Action != RP_WFP_ACTION_REJECT)) {
        return STATUS_INVALID_PARAMETER;
    }

    flow = RpFindFlowByRequestId(Decision->RequestId);
    if (flow == NULL) {
        return STATUS_NOT_FOUND;
    }

    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    if (!flow->Removed) {
        flow->Action = Decision->Action;
        flow->DecisionFlags = Decision->Flags;
        context = flow->CompletionContext;
        flow->CompletionContext = NULL;
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);

    if (context == NULL) {
        RpDereferenceFlow(flow);
        return STATUS_INVALID_DEVICE_STATE;
    }

    FwpsCompleteOperation0(context, NULL);
    RpDereferenceFlow(flow);
    return STATUS_SUCCESS;
}


NTSTATUS RpSetProxyReady(_In_ const RP_WFP_PROXY_READY* Ready)
{
    KIRQL oldIrql;
    PLIST_ENTRY entry;
    BOOLEAN value;

    if (Ready == NULL ||
        Ready->AbiVersion != RP_WFP_ABI_VERSION ||
        Ready->Size != sizeof(RP_WFP_PROXY_READY) ||
        Ready->Ready > 1) {
        return STATUS_INVALID_PARAMETER;
    }

    value = Ready->Ready != 0;
    KeAcquireSpinLock(&g_RpState.Lock, &oldIrql);
    g_RpState.ProxyReady = value;
    for (entry = g_RpState.Flows.Flink; entry != &g_RpState.Flows; entry = entry->Flink) {
        RP_FLOW* flow = CONTAINING_RECORD(entry, RP_FLOW, Link);
        if (!flow->Removed &&
            flow->Key.Protocol == IPPROTO_UDP &&
            flow->Key.DestinationPort == 53 &&
            (flow->DecisionFlags & RP_WFP_DECISION_FLAG_DNS_AUTO) != 0) {
            flow->Action = value ? RP_WFP_ACTION_PROXY : RP_WFP_ACTION_DIRECT;
            flow->LastSeen100ns = KeQueryInterruptTime();
        }
    }
    KeReleaseSpinLock(&g_RpState.Lock, oldIrql);
    return STATUS_SUCCESS;
}
