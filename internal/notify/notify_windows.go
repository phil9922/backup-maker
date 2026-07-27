// SPDX-License-Identifier: MIT

//go:build windows

package notify

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
)

// send raises a Windows toast through Windows PowerShell's WinRT bindings.
//
// WHY THIS ROUTE, and what it costs. A toast needs an AppUserModelID that is
// registered with the shell; a bare executable has none, so it borrows Windows
// PowerShell's — which is why the toast is attributed to "Windows PowerShell"
// rather than to backup-maker. Fixing that properly means installing a shortcut
// with an AppUserModelID, or packaging the app, neither of which belongs in a
// notification. The alternative was a third-party module (BurntToast), and a
// backup tool does not add a dependency to say "your backups stopped".
//
// It is powershell.exe deliberately, NOT pwsh: the WinRT projection used here
// ships with Windows PowerShell 5.1, which is present on every Windows 10/11
// machine, and is absent from PowerShell 7 without an extra SDK package. On
// anything older the script fails and, like every other failure in this
// package, is swallowed.
//
// There is no true sticky toast either: `duration=long` keeps it on screen for
// about 25 seconds instead of 7. Nothing is lost when it goes — Windows keeps
// toasts in the Action Center, where a missed one can still be found.
func send(ctx context.Context, u Urgency, title, body string) error {
	duration := "short"
	if u == Critical {
		duration = "long"
	}
	// PowerShell's own AppUserModelID, as registered in the Start menu.
	const appID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`
	script := strings.Join([]string{
		`$ErrorActionPreference = 'Stop'`,
		`[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null`,
		`$xml = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)`,
		`$text = $xml.GetElementsByTagName('text')`,
		`$text.Item(0).AppendChild($xml.CreateTextNode(` + psQuote(title) + `)) | Out-Null`,
		`$text.Item(1).AppendChild($xml.CreateTextNode(` + psQuote(body) + `)) | Out-Null`,
		`$xml.DocumentElement.SetAttribute('duration', '` + duration + `')`,
		`$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)`,
		`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier(` + psQuote(appID) + `).Show($toast)`,
		// The toast is handed to the shell asynchronously; exiting immediately
		// can lose it.
		`Start-Sleep -Milliseconds 500`,
	}, "; ")
	return run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}

// psQuote renders a Go string as a PowerShell single-quoted literal. Single
// quotes only: nothing inside is expanded, and the whole script then survives
// being passed as one Windows command-line argument without a double quote of
// its own to fight over.
func psQuote(s string) string {
	return `'` + strings.ReplaceAll(oneLine(s), `'`, `''`) + `'`
}

// hideConsole keeps a console window from flashing up when the daemon runs
// with no window of its own.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
