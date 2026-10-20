//go:build darwin && desktop

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"cyberagent-workbench/internal/app"
	"cyberagent-workbench/internal/apperror"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// trustedDesktopRendererHost pins the exact WKWebView authority: on macOS the
// renderer loads from the custom wails:// scheme whose host is "wails". The
// Wails AssetServer middleware has already enforced that host before the
// request reaches the in-process handler.
func trustedDesktopRendererHost() string {
	return "wails"
}

// checkDesktopPrerequisites returns nil on macOS: WKWebView ships with every
// supported macOS release (the Go 1.26 toolchain requires macOS 12 Monterey or
// newer), so the Desktop never probes, downloads, or installs a web runtime.
func checkDesktopPrerequisites() error {
	return nil
}

func reportDesktopStartupFailure(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	showDesktopStartupFailureDialog(desktopStartupFailureMessage(err))
}

// desktopStartupFailureMessage returns a bounded, path-free startup failure
// message. Normalization keeps private error detail out of the user-facing
// dialog; only the stable error code is shown.
func desktopStartupFailureMessage(err error) string {
	code := apperror.CodeOf(apperror.Normalize(err))
	return "Universal Code could not start.\n\nError code: " + string(code) +
		"\n\nLocal data was not deleted or reset. Keep it for diagnosis."
}

// showDesktopStartupFailureDialog shows the failure through the fixed
// /usr/bin/osascript display dialog command. The message is strictly escaped
// into a single AppleScript string literal, bounded, and best-effort: stderr
// already carries the error and a missing or failing osascript never changes
// the exit code. No download, installer, URL, or shell is involved.
func showDesktopStartupFailureDialog(message string) {
	if strings.TrimSpace(message) == "" || len(message) > 1024 {
		return
	}
	script := "display dialog " + appleScriptStringLiteral(message) +
		" buttons {\"OK\"} default button 1 with icon stop with title " +
		appleScriptStringLiteral(app.Name)
	command := exec.Command("/usr/bin/osascript", "-e", script)
	command.Stdin = nil
	_ = command.Run()
}

// appleScriptStringLiteral returns value as an AppleScript string literal
// without changing its meaning: backslashes, double quotes, CR, and LF are
// escaped and the result is wrapped in double quotes.
func appleScriptStringLiteral(value string) string {
	var builder strings.Builder
	builder.Grow(len(value) + 2)
	builder.WriteByte('"')
	for _, current := range value {
		switch current {
		case '\\':
			builder.WriteString("\\\\")
		case '"':
			builder.WriteString("\\\"")
		case '\r':
			builder.WriteString("\\r")
		case '\n':
			builder.WriteString("\\n")
		default:
			builder.WriteRune(current)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func applyDesktopPlatformOptions(appOptions *options.App) {
	// Wails omits the native titlebar and close button for a frameless window.
	// Hidden supplies the full-size content layout while keeping macOS
	// traffic lights, so override the shared Windows frameless setting.
	appOptions.Frameless = false
	appOptions.Mac = desktopMacOptions()
}

func desktopMacOptions() *mac.Options {
	return &mac.Options{
		// Hidden keeps the standard traffic lights aligned with the compact
		// React titlebar. HiddenInset adds an NSToolbar that lowers them.
		TitleBar: mac.TitleBarHidden(),
		// Follow the system appearance; the renderer applies its own
		// light/dark/glass tokens through the existing desktop bindings.
		Appearance:           mac.DefaultAppearance,
		WebviewIsTransparent: true,
		WindowIsTranslucent:  true,
		// Let web content handle Escape so React modals close before the
		// system fullscreen escape path can fire.
		DisableEscapeExitsFullscreen: true,
	}
}
