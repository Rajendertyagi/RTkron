//go:build windows
// +build windows

package tray

import (
    "context"
    "log"
    "os"
    "os/exec"
    "runtime"
    "time"

    "github.com/getlantern/systray"
    "golang.org/x/sys/windows/registry"
)

// StartTray sets up the tray and handles clicks
func StartTray(ctx context.Context, serverPort string, onQuit func()) {
    go func() {
        systray.Run(func() {
            onReadyTray(serverPort, onQuit)
        }, func() {
            // onExit
        })
    }()
}

func onReadyTray(serverPort string, onQuit func()) {
    systray.SetTitle("RTKron")
    systray.SetTooltip("RTKron — running")

    openItem := systray.AddMenuItem("Open UI", "Open the web UI in your browser")
    startupItem := systray.AddMenuItemCheckbox("Start with Windows", "Toggle start at login", isRegisteredStartup())
    systray.AddSeparator()
    quitItem := systray.AddMenuItem("Quit", "Quit RTKron")

    go func() {
        for {
            select {
            case <-openItem.ClickedCh:
                openBrowser("http://localhost:" + serverPort + "/")
            case <-startupItem.ClickedCh:
                enabled := !isRegisteredStartup()
                if err := setStartup(enabled); err != nil {
                    log.Printf("setStartup error: %v", err)
                } else {
                    startupItem.Check()
                    if !enabled {
                        startupItem.Uncheck()
                    }
                }
            case <-quitItem.ClickedCh:
                go func() {
                    time.Sleep(100 * time.Millisecond)
                    onQuit()
                    systray.Quit()
                }()
                return
            }
        }
    }()
}

func openBrowser(url string) {
    switch runtime.GOOS {
    case "windows":
        _ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
    case "darwin":
        _ = exec.Command("open", url).Start()
    default:
        _ = exec.Command("xdg-open", url).Start()
    }
}

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValueName = "RTKron"

func isRegisteredStartup() bool {
    k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
    if err != nil {
        return false
    }
    defer k.Close()
    _, _, err = k.GetStringValue(runValueName)
    return err == nil
}

func setStartup(enable bool) error {
    exePath, err := os.Executable()
    if err != nil {
        return err
    }
    k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
    if err != nil {
        return err
    }
    defer k.Close()
    if enable {
        return k.SetStringValue(runValueName, exePath)
    }
    return k.DeleteValue(runValueName)
}
