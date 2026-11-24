//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

const createNoWindow = 0x08000000

// hideWindow stops a black console window flashing up for FFmpeg.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func openBrowser(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	hideWindow(cmd)
	return cmd.Start()
}

func openPath(path string) error {
	return exec.Command("explorer", path).Start()
}

const pickScript = `
Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = 'Choose where to save the finished videos'
$d.SelectedPath = $env:VM_START
if ($d.ShowDialog($owner) -eq 'OK') { [Console]::Out.Write($d.SelectedPath) }
`

func pickFolder(start string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", pickScript)
	cmd.Env = append(os.Environ(), "VM_START="+start)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func showError(msg string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	box := user32.NewProc("MessageBoxW")
	text, _ := syscall.UTF16PtrFromString(msg)
	title, _ := syscall.UTF16PtrFromString("Video Merger")
	box.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
