//go:build windows

package gui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// Windows Agent Web runs in a browser, not inside the Wails v3 process.
// Native Wails windows use app.Dialog directly; local browser mode uses the
// existing Windows Forms OS picker. The HTTP route is strictly loopback-only.
const windowsFolderPicker = `
Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择要共享的文件夹'
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  [Console]::Write($dialog.SelectedPath)
}
$dialog.Dispose()
`

const windowsSavePicker = `
Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$dialog = New-Object System.Windows.Forms.SaveFileDialog
$dialog.Title = '保存远程文件'
$dialog.FileName = $env:RELAYPROXY_DIALOG_FILE_NAME
$dialog.OverwritePrompt = $false
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  [Console]::Write($dialog.FileName)
}
$dialog.Dispose()
`

func nativeChooseShareFolder(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-Command", windowsFolderPicker).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func nativeChooseDownloadPath(ctx context.Context, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\:") {
		return "", errors.New("invalid save file name")
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-Command", windowsSavePicker)
	cmd.Env = append(os.Environ(), "RELAYPROXY_DIALOG_FILE_NAME="+name)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
