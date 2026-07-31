//go:build !windows
// +build !windows

package tray

import (
    "context"
    "log"
)

// StartTray is a no-op fallback for non-Windows platforms
func StartTray(ctx context.Context, serverPort string, onQuit func()) {
    log.Println("Systray not implemented for this platform. Run headlessly.")
}
