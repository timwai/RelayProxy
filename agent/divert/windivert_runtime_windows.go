//go:build windows

package divert

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"relayproxy/internal/windivert"
)

// Reading/validating the embedded archive has no filesystem or driver effects.
// The returned map and slices are immutable.
var bundledWinDivertFiles = sync.OnceValues(func() (map[string][]byte, error) {
	if len(embeddedWinDivertArchive) == 0 {
		return nil, errors.New("此架构未内嵌 WinDivert，请使用 Windows x64 客户端")
	}
	files, err := windivert.RuntimeFiles(embeddedWinDivertArchive)
	if err != nil {
		return nil, fmt.Errorf("内嵌 WinDivert 校验失败: %w", err)
	}
	return files, nil
})

func winDivertDependencies(executable string) error {
	if _, err := winDivertFiles(executable); err == nil {
		return nil
	}
	_, err := bundledWinDivertFiles()
	return err
}

// extractEmbeddedWinDivert runs only when starting interception, after the
// elevation check. ProgramData avoids writing beside a portable/read-only EXE.
func extractEmbeddedWinDivert() (string, error) {
	files, err := bundledWinDivertFiles()
	if err != nil {
		return "", err
	}
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	root = filepath.Join(root, "RelayProxy-WinDivert")
	// Both levels are administrator-owned and writable only by Administrators
	// and SYSTEM. Refuse pre-existing user-owned folders and reparse points.
	if err := ensureWinDivertDirectory(root); err != nil {
		return "", err
	}
	directory := filepath.Join(root, windivert.Version+"-"+windivert.ArchiveSHA256[:16])
	if err := ensureWinDivertDirectory(directory); err != nil {
		return "", err
	}
	if err := materializeWinDivert(directory, files); err != nil {
		return "", fmt.Errorf("释放内嵌 WinDivert 失败: %w", err)
	}
	return filepath.Join(directory, "WinDivert.dll"), nil
}

const (
	winDivertDirectorySDDL = "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	// FILE_ALL_ACCESS = STANDARD_RIGHTS_REQUIRED | SYNCHRONIZE | 0x1ff.
	winDivertFileAllAccess windows.ACCESS_MASK = 0x001f01ff
)

func ensureWinDivertDirectory(directory string) error {
	descriptor, err := windows.SecurityDescriptorFromString(winDivertDirectorySDDL)
	if err != nil {
		return err
	}
	path, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(path, &attributes); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("创建 WinDivert 驱动目录失败（需要管理员权限）: %w", err)
	}
	if err := plainWinDivertPath(directory, true); err != nil {
		return err
	}
	actual, err := windows.GetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if err := validateWinDivertDirectorySecurity(actual); err != nil {
		return fmt.Errorf("WinDivert 驱动目录权限不安全 %s: %w", directory, err)
	}
	return nil
}

func validateWinDivertDirectorySecurity(descriptor *windows.SECURITY_DESCRIPTOR) error {
	if descriptor == nil {
		return errors.New("missing security descriptor")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || (!owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid)) {
		return errors.New("directory must be owned by Administrators or SYSTEM")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("directory permissions must not inherit user write access")
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return errors.New("directory must allow only Administrators and SYSTEM")
	}
	var administrators, system bool
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != winDivertFileAllAccess ||
			ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
			return errors.New("unexpected directory permission entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		administrators = administrators || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
		system = system || sid.IsWellKnown(windows.WinLocalSystemSid)
	}
	if !administrators || !system {
		return errors.New("directory must allow only Administrators and SYSTEM")
	}
	return nil
}

func plainWinDivertPath(path string, directory bool) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return fmt.Errorf("WinDivert 路径不能是链接或其他文件类型: %s", path)
	}
	return nil
}

// materializeWinDivert only accepts the verified, flat runtime file set. Each
// file is published atomically while a cross-process lock serializes extraction.
// Existing matching files are left in place even when another agent uses them.
func materializeWinDivert(directory string, files map[string][]byte) error {
	if err := plainWinDivertPath(directory, true); err != nil {
		return err
	}
	unlock, err := lockWinDivertDirectory(directory)
	if err != nil {
		return err
	}
	defer unlock()
	for name, contents := range files {
		if filepath.Base(name) != name || name == "." || len(contents) == 0 {
			return fmt.Errorf("invalid WinDivert runtime file: %s", name)
		}
		if err := writeWinDivertFile(filepath.Join(directory, name), contents); err != nil {
			return err
		}
	}
	return nil
}

func lockWinDivertDirectory(directory string) (func(), error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(directory, ".extract.lock"))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("WinDivert extraction lock must be a regular file")
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
		_ = windows.CloseHandle(handle)
	}, nil
}

func winDivertFileMatches(path string, contents []byte) (bool, error) {
	if err := plainWinDivertPath(path, false); err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return false, nil
		}
		return false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(contents))+1))
	return bytes.Equal(data, contents), err
}

func writeWinDivertFile(path string, contents []byte) error {
	if matches, err := winDivertFileMatches(path, contents); matches || err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".windivert-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		if matches, _ := winDivertFileMatches(path, contents); matches {
			return nil
		}
		return err
	}
	return nil
}
