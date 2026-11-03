//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"9router/proxy/internal/config"
	"golang.org/x/sys/windows"
)

func vbString(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func psString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// The task runs a windowless launcher in the same data scope as the tray.
// It starts the daemon and then the tray, without retaining either process.
func installTrayAutostart() error {
	gateway, err := gatewayPath()
	if err != nil {
		return err
	}
	trayExe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := config.ResolveDataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	launcher := filepath.Join(dir, "tray-autostart.vbs")
	contents := "Option Explicit\r\nDim shell, result\r\nSet shell = CreateObject(\"WScript.Shell\")\r\n" +
		"shell.CurrentDirectory = " + vbString(dir) + "\r\n" +
		"result = shell.Run(" + vbString(`"`+gateway+`" start`) + ", 0, True)\r\n" +
		"If result <> 0 Then WScript.Quit result\r\n" +
		"shell.Run " + vbString(`"`+trayExe+`"`) + ", 0, False\r\n"
	if err := os.WriteFile(launcher, []byte(contents), 0600); err != nil {
		return err
	}
	arguments := `//B //NoLogo "` + launcher + `"`
	script := `$ErrorActionPreference='Stop'; ` +
		`$user="$env:USERDOMAIN\$env:USERNAME"; ` +
		`$action=New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\wscript.exe" -Argument ` + psString(arguments) + `; ` +
		`$trigger=New-ScheduledTaskTrigger -AtLogOn -User $user; ` +
		`$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited; ` +
		`$settings=New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit (New-TimeSpan -Minutes 5) -MultipleInstances IgnoreNew; ` +
		`Register-ScheduledTask -TaskName '9router-go' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create auto-start task: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func showTrayError(err error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	box := user32.NewProc("MessageBoxW")
	message, _ := windows.UTF16PtrFromString(err.Error())
	title, _ := windows.UTF16PtrFromString("9Router Go")
	box.Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}
