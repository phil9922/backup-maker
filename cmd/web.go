// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/phil9922/backup-maker/internal/config"
	"github.com/phil9922/backup-maker/internal/daemon"
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Open the dashboard in your browser",
	RunE: func(cmd *cobra.Command, args []string) error {
		return openDashboard()
	},
}

func openDashboard() error {
	if _, err := daemon.Connect(); err != nil {
		return err
	}
	state, err := config.LoadState()
	if err != nil {
		return err
	}
	// /auth logs the browser in via cookie, then redirects to the dashboard.
	url := fmt.Sprintf("http://127.0.0.1:%d/auth?token=%s", state.DashboardPort, state.IPCToken)
	if err := openBrowser(url); err != nil {
		fmt.Println("Open this URL in your browser:")
		fmt.Println(url)
		return nil
	}
	fmt.Printf("Dashboard: http://127.0.0.1:%d\n", state.DashboardPort)
	return nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func init() {
	rootCmd.AddCommand(webCmd)
}
