//go:build darwin

package gui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

const chooseFolderAppleScript = `try
return POSIX path of (choose folder with prompt "选择要共享的文件夹")
on error number -128
return ""
end try`

const chooseSaveAppleScript = `on run argv
try
return POSIX path of (choose file name with prompt "保存远程文件" default name (item 1 of argv))
on error number -128
return ""
end try
end run`

func nativeChooseShareFolder(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "osascript", "-e", chooseFolderAppleScript).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
func nativeChooseDownloadPath(ctx context.Context, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", errors.New("invalid name")
	}
	out, err := exec.CommandContext(ctx, "osascript", "-e", chooseSaveAppleScript, name).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
