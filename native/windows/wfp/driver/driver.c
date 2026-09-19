#include "driver.h"

PDEVICE_OBJECT g_RpDevice = NULL;

const GUID RP_PROVIDER_GUID          = {0x6e2954b3,0x36e9,0x4d86,{0xb0,0x3a,0x47,0x47,0x2b,0xa5,0x3e,0x31}};
const GUID RP_SUBLAYER_GUID          = {0x354aaef2,0x61e9,0x47c8,{0x9d,0xd4,0x7a,0x4e,0xe8,0x44,0x33,0xa2}};
const GUID RP_DEVICE_CLASS_GUID      = {0x138951ac,0x1a21,0x401d,{0x9d,0xe5,0x13,0x2d,0xaa,0x62,0x7f,0x01}};
const GUID RP_CALLOUT_AUTH_V4        = {0xb1a44e41,0x7304,0x44bc,{0x8c,0x82,0x79,0xbd,0x13,0x57,0x0a,0x11}};
const GUID RP_CALLOUT_AUTH_V6        = {0x67ec3f69,0x850e,0x4dd4,{0x87,0x19,0x71,0x8a,0xa2,0xc4,0x4b,0x04}};
const GUID RP_CALLOUT_REDIRECT_V4    = {0xcff73665,0x86fb,0x4f4a,{0x8d,0x86,0x61,0xd5,0xa2,0xa1,0xe4,0xd5}};
const GUID RP_CALLOUT_REDIRECT_V6    = {0x78c1e6e8,0xa113,0x402e,{0xb2,0xb7,0x2c,0x2e,0xdf,0x79,0x2f,0x39}};
const GUID RP_CALLOUT_DATAGRAM_V4    = {0x56932702,0x6b73,0x43a0,{0x96,0xe2,0xb2,0x58,0xc1,0x8d,0x72,0x62}};
const GUID RP_CALLOUT_DATAGRAM_V6    = {0x8f07790b,0xc47d,0x4e4a,{0xaf,0x82,0xcf,0xc6,0x4d,0xb8,0x5c,0xe8}};
const GUID RP_CALLOUT_FLOW_V4        = {0x6a70b3c9,0x37ed,0x4a72,{0x8e,0xec,0x46,0xa0,0x47,0x65,0xc4,0xf2}};
const GUID RP_CALLOUT_FLOW_V6        = {0xf35d5d1a,0x4057,0x42cf,{0x94,0xaf,0xe8,0xd9,0x4e,0xab,0x43,0xb7}};
const GUID RP_CALLOUT_STREAM_V4      = {0x5429fc97,0x69bd,0x4eae,{0x86,0x63,0xf5,0x92,0xe1,0xd7,0xc6,0x8a}};
const GUID RP_CALLOUT_STREAM_V6      = {0x6292c06c,0xda4e,0x4a62,{0xb2,0x10,0x93,0xd6,0x2e,0xe5,0x58,0xea}};

static NTSTATUS RpCompleteIrp(_In_ PIRP Irp, _In_ NTSTATUS Status, _In_ ULONG_PTR Information)
{
    Irp->IoStatus.Status = Status;
    Irp->IoStatus.Information = Information;
    IoCompleteRequest(Irp, IO_NO_INCREMENT);
    return Status;
}

static NTSTATUS RpCreateClose(_In_ PDEVICE_OBJECT DeviceObject, _In_ PIRP Irp)
{
    UNREFERENCED_PARAMETER(DeviceObject);
    return RpCompleteIrp(Irp, STATUS_SUCCESS, 0);
}

static NTSTATUS RpCleanup(_In_ PDEVICE_OBJECT DeviceObject, _In_ PIRP Irp)
{
    PIO_STACK_LOCATION stack;
    UNREFERENCED_PARAMETER(DeviceObject);
    stack = IoGetCurrentIrpStackLocation(Irp);
    RpControllerCleanup(stack->FileObject);
    return RpCompleteIrp(Irp, STATUS_SUCCESS, 0);
}

static NTSTATUS RpDeviceControl(_In_ PDEVICE_OBJECT DeviceObject, _In_ PIRP Irp)
{
    PIO_STACK_LOCATION stack;
    ULONG code;
    ULONG inLength;
    ULONG outLength;
    VOID* buffer;
    NTSTATUS status = STATUS_INVALID_DEVICE_REQUEST;
    ULONG_PTR information = 0;

    UNREFERENCED_PARAMETER(DeviceObject);

    stack = IoGetCurrentIrpStackLocation(Irp);
    code = stack->Parameters.DeviceIoControl.IoControlCode;
    inLength = stack->Parameters.DeviceIoControl.InputBufferLength;
    outLength = stack->Parameters.DeviceIoControl.OutputBufferLength;
    buffer = Irp->AssociatedIrp.SystemBuffer;

    switch (code) {
    case RP_WFP_IOCTL_GET_VERSION:
        if (buffer == NULL || outLength < sizeof(RP_WFP_VERSION)) {
            status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        {
            RP_WFP_VERSION* version = (RP_WFP_VERSION*)buffer;
            RtlZeroMemory(version, sizeof(*version));
            version->AbiVersion = RP_WFP_ABI_VERSION;
            version->Size = sizeof(*version);
            version->Features = RP_WFP_FEATURE_TCP |
                                RP_WFP_FEATURE_UDP |
                                RP_WFP_FEATURE_IPV6 |
                                RP_WFP_FEATURE_SYSTEM_IDENTITY;
            information = sizeof(*version);
            status = STATUS_SUCCESS;
        }
        break;

    case RP_WFP_IOCTL_SET_CONFIG:
        if (buffer == NULL || inLength < sizeof(RP_WFP_CONFIG)) {
            status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        status = RpConfigureController(Irp, (const RP_WFP_CONFIG*)buffer);
        break;

    case RP_WFP_IOCTL_GET_EVENT:
        if (!RpIsControllerFile(stack->FileObject)) {
            status = STATUS_ACCESS_DENIED;
            break;
        }
        if (buffer == NULL || outLength == 0) {
            status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        status = RpReadEvent(buffer, outLength, &information);
        break;

    case RP_WFP_IOCTL_SET_DECISION:
        if (!RpIsControllerFile(stack->FileObject)) {
            status = STATUS_ACCESS_DENIED;
            break;
        }
        if (buffer == NULL || inLength < sizeof(RP_WFP_DECISION)) {
            status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        status = RpApplyDecision((const RP_WFP_DECISION*)buffer);
        break;

    case RP_WFP_IOCTL_INJECT_UDP:
        if (!RpIsControllerFile(stack->FileObject)) {
            status = STATUS_ACCESS_DENIED;
            break;
        }
        if (buffer == NULL || inLength < FIELD_OFFSET(RP_WFP_UDP_INJECT, Payload)) {
            status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        status = RpInjectUdp((const RP_WFP_UDP_INJECT*)buffer, inLength);
        break;

    case RP_WFP_IOCTL_HEARTBEAT:
        if (RpIsControllerFile(stack->FileObject) &&
            IoGetRequestorProcessId(Irp) == g_RpState.ControllerPid) {
            RpHeartbeat();
            status = STATUS_SUCCESS;
        } else {
            status = STATUS_ACCESS_DENIED;
        }
        break;

    case RP_WFP_IOCTL_STOP:
        if (!g_RpState.ControllerActive ||
            (RpIsControllerFile(stack->FileObject) &&
             IoGetRequestorProcessId(Irp) == g_RpState.ControllerPid)) {
            RpControllerFailOpen();
            status = STATUS_SUCCESS;
        } else {
            status = STATUS_ACCESS_DENIED;
        }
        break;

    case RP_WFP_IOCTL_RELEASE:
        if (!RpIsControllerFile(stack->FileObject)) {
            status = STATUS_ACCESS_DENIED;
            break;
        }
        if (buffer == NULL || inLength < sizeof(RP_WFP_RELEASE)) {
            status = STATUS_BUFFER_TOO_SMALL;
            break;
        }
        {
            const RP_WFP_RELEASE* release = (const RP_WFP_RELEASE*)buffer;
            RP_FLOW* flow;
            if (release->AbiVersion != RP_WFP_ABI_VERSION ||
                release->Size != sizeof(RP_WFP_RELEASE) ||
                release->RequestId == 0) {
                status = STATUS_INVALID_PARAMETER;
                break;
            }
            flow = RpFindFlowByRequestId(release->RequestId);
            if (flow == NULL) {
                status = STATUS_NOT_FOUND;
                break;
            }
            RpRemoveFlow(flow, FALSE);
            RpDereferenceFlow(flow);
            status = STATUS_SUCCESS;
        }
        break;

    default:
        status = STATUS_INVALID_DEVICE_REQUEST;
        break;
    }

    return RpCompleteIrp(Irp, status, information);
}

static VOID RpUnload(_In_ PDRIVER_OBJECT DriverObject)
{
    UNICODE_STRING symbolicLink;

    RpControllerFailOpen();
    RpWfpStop();
    RpStateShutdown();

    RtlInitUnicodeString(&symbolicLink, L"\\DosDevices\\RelayProxyWfp");
    IoDeleteSymbolicLink(&symbolicLink);

    if (g_RpDevice != NULL) {
        IoDeleteDevice(g_RpDevice);
        g_RpDevice = NULL;
    }

    UNREFERENCED_PARAMETER(DriverObject);
}

NTSTATUS DriverEntry(_In_ PDRIVER_OBJECT DriverObject, _In_ PUNICODE_STRING RegistryPath)
{
    UNICODE_STRING deviceName;
    UNICODE_STRING symbolicLink;
    UNICODE_STRING sddl;
    NTSTATUS status;

    UNREFERENCED_PARAMETER(RegistryPath);

    status = RpStateInitialize();
    if (!NT_SUCCESS(status)) {
        return status;
    }

    RtlInitUnicodeString(&deviceName, L"\\Device\\RelayProxyWfp");
    RtlInitUnicodeString(&sddl, L"D:P(A;;GA;;;SY)(A;;GA;;;BA)");

    status = IoCreateDeviceSecure(
        DriverObject,
        0,
        &deviceName,
        FILE_DEVICE_NETWORK,
        FILE_DEVICE_SECURE_OPEN,
        FALSE,
        &sddl,
        &RP_DEVICE_CLASS_GUID,
        &g_RpDevice);
    if (!NT_SUCCESS(status)) {
        RpStateShutdown();
        return status;
    }

    g_RpDevice->Flags |= DO_BUFFERED_IO;

    RtlInitUnicodeString(&symbolicLink, L"\\DosDevices\\RelayProxyWfp");
    status = IoCreateSymbolicLink(&symbolicLink, &deviceName);
    if (!NT_SUCCESS(status)) {
        IoDeleteDevice(g_RpDevice);
        g_RpDevice = NULL;
        RpStateShutdown();
        return status;
    }

    DriverObject->MajorFunction[IRP_MJ_CREATE] = RpCreateClose;
    DriverObject->MajorFunction[IRP_MJ_CLOSE] = RpCreateClose;
    DriverObject->MajorFunction[IRP_MJ_CLEANUP] = RpCleanup;
    DriverObject->MajorFunction[IRP_MJ_DEVICE_CONTROL] = RpDeviceControl;
    DriverObject->DriverUnload = RpUnload;

    status = RpWfpStart(g_RpDevice);
    if (!NT_SUCCESS(status)) {
        IoDeleteSymbolicLink(&symbolicLink);
        IoDeleteDevice(g_RpDevice);
        g_RpDevice = NULL;
        RpStateShutdown();
        return status;
    }

    g_RpDevice->Flags &= ~DO_DEVICE_INITIALIZING;
    return STATUS_SUCCESS;
}
