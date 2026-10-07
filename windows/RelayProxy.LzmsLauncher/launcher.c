#define UNICODE
#define _UNICODE

#include <windows.h>
#include <compressapi.h>
#include <bcrypt.h>
#include <shellapi.h>
#include <shlobj.h>
#include <strsafe.h>
#include <stdint.h>

#pragma comment(lib, "Cabinet.lib")
#pragma comment(lib, "Bcrypt.lib")
#pragma comment(lib, "Shell32.lib")
#pragma comment(lib, "User32.lib")

#define FOOTER_MAGIC "RELAYPROXY_GUI4!"
#define LZMS_BLOCK_SIZE (64u * 1024u * 1024u)
#define CACHE_PRODUCT L"RelayProxy"
#define CACHE_SUBDIR L"native-host-lzms-c-experiment"
#define NATIVE_HOST_NAME L"RelayProxy.NativeHost.exe"
#define LAUNCHER_ENV L"RELAYPROXY_LAUNCHER_PATH"

#pragma pack(push, 1)
typedef struct RelayFooter {
    char magic[16];
    uint64_t compressed_size;
    uint64_t raw_size;
    unsigned char hash[32];
} RelayFooter;
#pragma pack(pop)

static void fail_box(const wchar_t *message, DWORD error) {
    wchar_t buffer[1024];
    if (error != 0) {
        StringCchPrintfW(buffer, ARRAYSIZE(buffer), L"%s\n\nWindows error: %lu", message, error);
    } else {
        StringCchCopyW(buffer, ARRAYSIZE(buffer), message);
    }
    MessageBoxW(NULL, buffer, L"RelayProxy startup failed", MB_OK | MB_ICONERROR);
}

static BOOL read_exact(HANDLE file, void *buffer, uint64_t size) {
    unsigned char *cursor = (unsigned char *)buffer;
    uint64_t remaining = size;
    while (remaining > 0) {
        DWORD chunk = remaining > 16u * 1024u * 1024u ? 16u * 1024u * 1024u : (DWORD)remaining;
        DWORD done = 0;
        if (!ReadFile(file, cursor, chunk, &done, NULL) || done != chunk) {
            return FALSE;
        }
        cursor += done;
        remaining -= done;
    }
    return TRUE;
}

static BOOL write_exact(HANDLE file, const void *buffer, uint64_t size) {
    const unsigned char *cursor = (const unsigned char *)buffer;
    uint64_t remaining = size;
    while (remaining > 0) {
        DWORD chunk = remaining > 16u * 1024u * 1024u ? 16u * 1024u * 1024u : (DWORD)remaining;
        DWORD done = 0;
        if (!WriteFile(file, cursor, chunk, &done, NULL) || done != chunk) {
            return FALSE;
        }
        cursor += done;
        remaining -= done;
    }
    return TRUE;
}

static BOOL sha256_buffer(const unsigned char *data, uint64_t size, unsigned char out[32]) {
    BCRYPT_ALG_HANDLE algorithm = NULL;
    BCRYPT_HASH_HANDLE hash = NULL;
    PUCHAR object = NULL;
    DWORD object_size = 0;
    DWORD result_size = 0;
    NTSTATUS status;
    BOOL ok = FALSE;

    status = BCryptOpenAlgorithmProvider(&algorithm, BCRYPT_SHA256_ALGORITHM, NULL, 0);
    if (status < 0) goto cleanup;
    status = BCryptGetProperty(algorithm, BCRYPT_OBJECT_LENGTH, (PUCHAR)&object_size, sizeof(object_size), &result_size, 0);
    if (status < 0 || object_size == 0) goto cleanup;

    object = (PUCHAR)HeapAlloc(GetProcessHeap(), 0, object_size);
    if (!object) goto cleanup;
    status = BCryptCreateHash(algorithm, &hash, object, object_size, NULL, 0, 0);
    if (status < 0) goto cleanup;

    while (size > 0) {
        ULONG chunk = size > 16u * 1024u * 1024u ? 16u * 1024u * 1024u : (ULONG)size;
        status = BCryptHashData(hash, (PUCHAR)data, chunk, 0);
        if (status < 0) goto cleanup;
        data += chunk;
        size -= chunk;
    }
    status = BCryptFinishHash(hash, out, 32, 0);
    if (status < 0) goto cleanup;
    ok = TRUE;

cleanup:
    if (hash) BCryptDestroyHash(hash);
    if (object) HeapFree(GetProcessHeap(), 0, object);
    if (algorithm) BCryptCloseAlgorithmProvider(algorithm, 0);
    return ok;
}

static void hash_hex(const unsigned char hash[32], wchar_t out[65]) {
    static const wchar_t digits[] = L"0123456789abcdef";
    for (int i = 0; i < 32; ++i) {
        out[i * 2] = digits[(hash[i] >> 4) & 0x0f];
        out[i * 2 + 1] = digits[hash[i] & 0x0f];
    }
    out[64] = L'\0';
}

static BOOL file_exists(const wchar_t *path) {
    DWORD attr = GetFileAttributesW(path);
    return attr != INVALID_FILE_ATTRIBUTES && !(attr & FILE_ATTRIBUTE_DIRECTORY);
}

static BOOL ensure_directory(const wchar_t *path) {
    int result = SHCreateDirectoryExW(NULL, path, NULL);
    return result == ERROR_SUCCESS || result == ERROR_ALREADY_EXISTS || result == ERROR_FILE_EXISTS;
}

static BOOL decompress_lzms(const unsigned char *compressed, SIZE_T compressed_size, unsigned char *output, SIZE_T output_size) {
    DECOMPRESSOR_HANDLE decompressor = NULL;
    SIZE_T written = 0;
    DWORD block_size = LZMS_BLOCK_SIZE;
    BOOL ok = FALSE;

    if (!CreateDecompressor(COMPRESS_ALGORITHM_LZMS, NULL, &decompressor)) {
        return FALSE;
    }
    if (!SetDecompressorInformation(
            decompressor,
            COMPRESS_INFORMATION_CLASS_BLOCK_SIZE,
            &block_size,
            sizeof(block_size))) {
        CloseDecompressor(decompressor);
        return FALSE;
    }
    ok = Decompress(
        decompressor,
        compressed,
        compressed_size,
        output,
        output_size,
        &written);
    CloseDecompressor(decompressor);
    return ok && written == output_size;
}

static BOOL build_cache_paths(
    const RelayFooter *footer,
    wchar_t host_path[32768],
    wchar_t ready_path[32768],
    wchar_t cache_dir[32768]) {
    wchar_t base[32768];
    wchar_t hash[65];
    DWORD length = GetEnvironmentVariableW(L"LOCALAPPDATA", base, ARRAYSIZE(base));
    if (length == 0 || length >= ARRAYSIZE(base)) {
        length = GetTempPathW(ARRAYSIZE(base), base);
        if (length == 0 || length >= ARRAYSIZE(base)) return FALSE;
        while (length > 0 && (base[length - 1] == L'\\' || base[length - 1] == L'/')) {
            base[--length] = L'\0';
        }
    }

    hash_hex(footer->hash, hash);
    if (FAILED(StringCchPrintfW(
            cache_dir,
            32768,
            L"%s\\%s\\%s\\%s",
            base,
            CACHE_PRODUCT,
            CACHE_SUBDIR,
            hash))) return FALSE;
    if (FAILED(StringCchPrintfW(host_path, 32768, L"%s\\%s", cache_dir, NATIVE_HOST_NAME))) return FALSE;
    if (FAILED(StringCchPrintfW(ready_path, 32768, L"%s\\.ready", cache_dir))) return FALSE;
    return TRUE;
}

static BOOL ensure_host(
    HANDLE self,
    const RelayFooter *footer,
    uint64_t payload_offset,
    const wchar_t *host_path,
    const wchar_t *ready_path,
    const wchar_t *cache_dir) {
    if (file_exists(host_path) && file_exists(ready_path)) {
        return TRUE;
    }

    if (footer->compressed_size == 0 || footer->raw_size == 0 ||
        footer->compressed_size > (uint64_t)(SIZE_T)-1 ||
        footer->raw_size > (uint64_t)(SIZE_T)-1) {
        SetLastError(ERROR_INVALID_DATA);
        return FALSE;
    }

    unsigned char *compressed = (unsigned char *)VirtualAlloc(
        NULL, (SIZE_T)footer->compressed_size, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    unsigned char *raw = (unsigned char *)VirtualAlloc(
        NULL, (SIZE_T)footer->raw_size, MEM_COMMIT | MEM_RESERVE, PAGE_READWRITE);
    BOOL ok = FALSE;
    HANDLE output = INVALID_HANDLE_VALUE;
    HANDLE ready = INVALID_HANDLE_VALUE;
    wchar_t temp_path[32768];
    unsigned char actual_hash[32];

    if (!compressed || !raw) {
        SetLastError(ERROR_NOT_ENOUGH_MEMORY);
        goto cleanup;
    }

    LARGE_INTEGER offset;
    offset.QuadPart = (LONGLONG)payload_offset;
    if (!SetFilePointerEx(self, offset, NULL, FILE_BEGIN)) goto cleanup;
    if (!read_exact(self, compressed, footer->compressed_size)) goto cleanup;
    if (!decompress_lzms(compressed, (SIZE_T)footer->compressed_size, raw, (SIZE_T)footer->raw_size)) goto cleanup;
    if (!sha256_buffer(raw, footer->raw_size, actual_hash)) {
        SetLastError(ERROR_CRC);
        goto cleanup;
    }
    if (memcmp(actual_hash, footer->hash, 32) != 0) {
        SetLastError(ERROR_CRC);
        goto cleanup;
    }

    if (!ensure_directory(cache_dir)) goto cleanup;
    if (FAILED(StringCchPrintfW(temp_path, ARRAYSIZE(temp_path), L"%s\\%s.tmp-%lu", cache_dir, NATIVE_HOST_NAME, GetCurrentProcessId()))) {
        SetLastError(ERROR_BUFFER_OVERFLOW);
        goto cleanup;
    }

    output = CreateFileW(temp_path, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
    if (output == INVALID_HANDLE_VALUE) goto cleanup;
    if (!write_exact(output, raw, footer->raw_size)) goto cleanup;
    if (!FlushFileBuffers(output)) goto cleanup;
    CloseHandle(output);
    output = INVALID_HANDLE_VALUE;

    if (!MoveFileExW(temp_path, host_path, MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH)) goto cleanup;

    ready = CreateFileW(ready_path, GENERIC_WRITE, 0, NULL, CREATE_ALWAYS, FILE_ATTRIBUTE_HIDDEN, NULL);
    if (ready == INVALID_HANDLE_VALUE) goto cleanup;
    {
        const char marker[] = "ready";
        DWORD done = 0;
        if (!WriteFile(ready, marker, (DWORD)(sizeof(marker) - 1), &done, NULL) || done != sizeof(marker) - 1) goto cleanup;
    }
    CloseHandle(ready);
    ready = INVALID_HANDLE_VALUE;
    ok = TRUE;

cleanup:
    if (output != INVALID_HANDLE_VALUE) CloseHandle(output);
    if (ready != INVALID_HANDLE_VALUE) CloseHandle(ready);
    if (compressed) VirtualFree(compressed, 0, MEM_RELEASE);
    if (raw) VirtualFree(raw, 0, MEM_RELEASE);
    return ok;
}

static SIZE_T quoted_arg_size(const wchar_t *arg) {
    SIZE_T size = 3;
    const wchar_t *p = arg;
    while (*p) {
        if (*p == L'\\' || *p == L'"') size += 2;
        else size += 1;
        ++p;
    }
    return size;
}

static wchar_t *append_quoted_arg(wchar_t *out, const wchar_t *arg) {
    *out++ = L'"';
    unsigned int backslashes = 0;
    for (const wchar_t *p = arg; ; ++p) {
        if (*p == L'\\') {
            ++backslashes;
            continue;
        }
        if (*p == L'"') {
            while (backslashes-- > 0) {
                *out++ = L'\\';
                *out++ = L'\\';
            }
            backslashes = 0;
            *out++ = L'\\';
            *out++ = L'"';
            continue;
        }
        if (*p == L'\0') {
            while (backslashes-- > 0) {
                *out++ = L'\\';
                *out++ = L'\\';
            }
            break;
        }
        while (backslashes-- > 0) *out++ = L'\\';
        backslashes = 0;
        *out++ = *p;
    }
    *out++ = L'"';
    return out;
}

static DWORD launch_host(const wchar_t *self_path, const wchar_t *host_path) {
    int argc = 0;
    LPWSTR *argv = CommandLineToArgvW(GetCommandLineW(), &argc);
    if (!argv || argc < 1) {
        if (argv) LocalFree(argv);
        SetLastError(ERROR_INVALID_COMMAND_LINE);
        return (DWORD)-1;
    }

    SIZE_T chars = quoted_arg_size(host_path) + 1;
    for (int i = 1; i < argc; ++i) chars += quoted_arg_size(argv[i]) + 1;
    wchar_t *command = (wchar_t *)HeapAlloc(GetProcessHeap(), HEAP_ZERO_MEMORY, chars * sizeof(wchar_t));
    if (!command) {
        LocalFree(argv);
        SetLastError(ERROR_NOT_ENOUGH_MEMORY);
        return (DWORD)-1;
    }

    wchar_t *cursor = command;
    cursor = append_quoted_arg(cursor, host_path);
    for (int i = 1; i < argc; ++i) {
        *cursor++ = L' ';
        cursor = append_quoted_arg(cursor, argv[i]);
    }
    *cursor = L'\0';
    LocalFree(argv);

    if (!SetEnvironmentVariableW(LAUNCHER_ENV, self_path)) {
        HeapFree(GetProcessHeap(), 0, command);
        return (DWORD)-1;
    }

    STARTUPINFOW startup;
    PROCESS_INFORMATION process;
    ZeroMemory(&startup, sizeof(startup));
    ZeroMemory(&process, sizeof(process));
    startup.cb = sizeof(startup);

    BOOL started = CreateProcessW(
        host_path,
        command,
        NULL,
        NULL,
        FALSE,
        0,
        NULL,
        NULL,
        &startup,
        &process);
    HeapFree(GetProcessHeap(), 0, command);
    if (!started) return (DWORD)-1;

    CloseHandle(process.hThread);
    WaitForSingleObject(process.hProcess, INFINITE);
    DWORD exit_code = 1;
    GetExitCodeProcess(process.hProcess, &exit_code);
    CloseHandle(process.hProcess);
    return exit_code;
}

int WINAPI wWinMain(HINSTANCE instance, HINSTANCE previous, PWSTR command_line, int show) {
    (void)instance;
    (void)previous;
    (void)command_line;
    (void)show;

    wchar_t self_path[32768];
    DWORD self_length = GetModuleFileNameW(NULL, self_path, ARRAYSIZE(self_path));
    if (self_length == 0 || self_length >= ARRAYSIZE(self_path)) {
        fail_box(L"Unable to resolve launcher path.", GetLastError());
        return 1;
    }

    HANDLE self = CreateFileW(self_path, GENERIC_READ, FILE_SHARE_READ, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (self == INVALID_HANDLE_VALUE) {
        fail_box(L"Unable to open launcher.", GetLastError());
        return 1;
    }

    LARGE_INTEGER file_size;
    RelayFooter footer;
    BOOL ok = FALSE;
    if (!GetFileSizeEx(self, &file_size) || file_size.QuadPart < (LONGLONG)sizeof(footer)) {
        fail_box(L"Launcher payload is incomplete.", GetLastError());
        CloseHandle(self);
        return 1;
    }

    LARGE_INTEGER footer_position;
    footer_position.QuadPart = file_size.QuadPart - (LONGLONG)sizeof(footer);
    if (!SetFilePointerEx(self, footer_position, NULL, FILE_BEGIN) ||
        !read_exact(self, &footer, sizeof(footer)) ||
        memcmp(footer.magic, FOOTER_MAGIC, 16) != 0) {
        fail_box(L"Launcher payload footer is invalid.", ERROR_INVALID_DATA);
        CloseHandle(self);
        return 1;
    }

    uint64_t payload_offset = (uint64_t)file_size.QuadPart - sizeof(footer) - footer.compressed_size;
    if (footer.compressed_size == 0 ||
        footer.raw_size == 0 ||
        footer.compressed_size > (uint64_t)file_size.QuadPart - sizeof(footer)) {
        fail_box(L"Launcher payload size is invalid.", ERROR_INVALID_DATA);
        CloseHandle(self);
        return 1;
    }

    wchar_t host_path[32768];
    wchar_t ready_path[32768];
    wchar_t cache_dir[32768];
    if (!build_cache_paths(&footer, host_path, ready_path, cache_dir)) {
        fail_box(L"Unable to build RelayProxy cache path.", ERROR_BUFFER_OVERFLOW);
        CloseHandle(self);
        return 1;
    }

    ok = ensure_host(self, &footer, payload_offset, host_path, ready_path, cache_dir);
    DWORD last_error = GetLastError();
    CloseHandle(self);
    if (!ok) {
        fail_box(L"Unable to extract RelayProxy NativeHost.", last_error);
        return 1;
    }

    DWORD exit_code = launch_host(self_path, host_path);
    if (exit_code == (DWORD)-1) {
        fail_box(L"Unable to start RelayProxy NativeHost.", GetLastError());
        return 1;
    }
    return (int)exit_code;
}
