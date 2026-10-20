//go:build darwin && desktop

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"

	"github.com/wailsapp/wails/v2/pkg/options"
)

func TestTrustedDesktopRendererHostPinsDarwinSchemeAuthority(t *testing.T) {
	if trustedDesktopRendererHost() != "wails" {
		t.Fatalf("unexpected macOS renderer host: %q", trustedDesktopRendererHost())
	}
	request := httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/health", nil)
	request.Host = "wails.localhost"
	request.Header.Set("User-Agent", "PrayuDesktopTest/1.0 wails.io")
	request.URL.Scheme = ""
	request.URL.Host = ""
	request.RequestURI = request.URL.RequestURI()
	called := false
	handler := inProcessAPIHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("Windows authority reached API on macOS: status=%d called=%t",
			response.Code, called)
	}
}

func TestDesktopStartupFailureMessageIsBoundedAndPathFree(t *testing.T) {
	private := apperror.Wrap(apperror.CodeFailedPrecondition, "database validation failed",
		errors.New("/Users/private/cyberagent.db"))
	message := desktopStartupFailureMessage(private)
	if !strings.Contains(message, string(apperror.CodeFailedPrecondition)) ||
		strings.Contains(message, "private") || strings.Contains(message, "cyberagent.db") ||
		len(message) > 256 {
		t.Fatalf("unsafe startup failure message: %q", message)
	}
}

func TestDesktopMacOptionsPinTransparentTitlebar(t *testing.T) {
	macOptions := desktopMacOptions()
	if macOptions == nil || macOptions.TitleBar == nil ||
		!macOptions.TitleBar.HideTitle || !macOptions.TitleBar.FullSizeContent ||
		!macOptions.WebviewIsTransparent || !macOptions.WindowIsTranslucent {
		t.Fatalf("macOS transparent titlebar boundary is incomplete: %#v", macOptions)
	}
	if !macOptions.DisableEscapeExitsFullscreen {
		t.Fatalf("Escape can exit fullscreen before React modals: %#v", macOptions)
	}
	if macOptions.Appearance != "" || macOptions.ContentProtection ||
		macOptions.OnFileOpen != nil || macOptions.OnUrlOpen != nil {
		t.Fatalf("macOS options drifted outside the desktop boundary: %#v", macOptions)
	}
}

func TestApplyDesktopPlatformOptionsPreservesMacNativeWindowControls(t *testing.T) {
	// Match the shared shell's Windows-style chrome before platform overrides.
	appOptions := &options.App{Frameless: true}
	applyDesktopPlatformOptions(appOptions)
	if appOptions.Mac == nil || appOptions.Windows != nil || appOptions.Linux != nil {
		t.Fatalf("platform options drifted across operating systems: %#v", appOptions)
	}
	if appOptions.Frameless || appOptions.Mac.TitleBar == nil ||
		appOptions.Mac.TitleBar.HideTitleBar || appOptions.Mac.TitleBar.UseToolbar ||
		!appOptions.Mac.TitleBar.FullSizeContent || !appOptions.Mac.TitleBar.HideTitle {
		t.Fatalf("macOS native traffic lights lost their compact titlebar: %#v", appOptions)
	}
}

func TestCheckDesktopPrerequisitesNeedsNoWebRuntime(t *testing.T) {
	if err := checkDesktopPrerequisites(); err != nil {
		t.Fatalf("WKWebView prerequisite check failed: %v", err)
	}
}

func TestAppleScriptStringLiteralEscapesStrictly(t *testing.T) {
	got := appleScriptStringLiteral("line one \"quoted\" \\ path\nline two")
	want := "\"line one \\\"quoted\\\" \\\\ path\\nline two\""
	if got != want {
		t.Fatalf("appleScriptStringLiteral = %q, want %q", got, want)
	}
	if strings.Contains(got[1:len(got)-1], "\n") || strings.Contains(got[1:len(got)-1], "\r") {
		t.Fatalf("unescaped newline escaped into the dialog script: %q", got)
	}
	if appleScriptStringLiteral("") != "\"\"" {
		t.Fatal("empty message did not produce an empty literal")
	}
}
