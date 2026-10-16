//go:build windows

package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixtureRect struct{ Left, Top, Right, Bottom int }
type fixtureState struct {
	PID              int         `json:"pid"`
	HWND             string      `json:"hwnd"`
	Text             string      `json:"text"`
	Clicks           int         `json:"clicks"`
	FirstVisibleLine int         `json:"first_visible_line"`
	EditRect         fixtureRect `json:"edit_rect"`
	ButtonRect       fixtureRect `json:"button_rect"`
}

// TestLiveWindowsFixture requires all three explicit environment variables. It
// launches and controls only its own fixture, using the production executable's
// SDK stdio endpoint. Ordinary go test runs skip it before launching anything.
func TestLiveWindowsFixture(t *testing.T) {
	production := os.Getenv("UNIVERSAL_CODE_COMPUTER_USE_LIVE_EXE")
	fixture := os.Getenv("UNIVERSAL_CODE_COMPUTER_USE_LIVE_FIXTURE")
	output := os.Getenv("UNIVERSAL_CODE_COMPUTER_USE_LIVE_OUTPUT")
	if production == "" || fixture == "" || output == "" {
		t.Skip("set LIVE_EXE, LIVE_FIXTURE and LIVE_OUTPUT under UNIVERSAL_CODE_COMPUTER_USE_ to enable own-fixture desktop verification")
	}
	for name, path := range map[string]string{"production executable": production, "fixture executable": fixture, "output directory": output} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must be an absolute path", name)
		}
	}
	for name, path := range map[string]string{"production executable": production, "fixture executable": fixture} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			t.Fatalf("%s is not a file", name)
		}
	}
	if strings.EqualFold(filepath.Clean(production), filepath.Clean(fixture)) {
		t.Fatal("fixture must be a separate executable")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	runDir, err := os.MkdirTemp(output, "computer-use-live-")
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(runDir, "fixture-state.json")
	manualFocus := os.Getenv("UNIVERSAL_CODE_COMPUTER_USE_LIVE_MANUAL_FOCUS") == "1"
	timeout := 90 * time.Second
	if manualFocus {
		timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fixtureCmd := exec.CommandContext(ctx, fixture, "-state", statePath)
	// Hide only the console. STARTF_USESHOWWINDOW/SW_HIDE would also override
	// the fixture's first ShowWindow call and leave its actual UI invisible.
	fixtureCmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if err = fixtureCmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	// Process.Kill uses the handle retained by os/exec. No process-name lookup,
	// PID-reopening, desktop-wide close, or unrelated-window cleanup is used.
	defer func() { _ = fixtureCmd.Process.Kill(); _ = fixtureCmd.Wait() }()
	fixturePID := fixtureCmd.Process.Pid
	readState := func() (fixtureState, error) {
		var state fixtureState
		data, err := os.ReadFile(statePath)
		if err != nil {
			return state, err
		}
		err = json.Unmarshal(data, &state)
		if err == nil && state.PID != fixturePID {
			return state, fmt.Errorf("state belongs to a different process")
		}
		return state, err
	}
	awaitState := func(label string, predicate func(fixtureState) bool) fixtureState {
		t.Helper()
		deadline := time.NewTimer(8 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			if state, err := readState(); err == nil && predicate(state) {
				return state
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: %v", label, ctx.Err())
			case <-deadline.C:
				t.Fatalf("fixture did not reach %s", label)
			case <-tick.C:
			}
		}
	}
	initial := awaitState("ready state", func(s fixtureState) bool {
		return s.HWND != "" && s.EditRect.Right > s.EditRect.Left && s.ButtonRect.Right > s.ButtonRect.Left
	})
	cmd := exec.CommandContext(ctx, production, Arg)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	client := mcp.NewClient(&mcp.Implementation{Name: "universal-code-live-fixture-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil)
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		t.Fatalf("connect production computer-use helper: %v", err)
	}
	defer session.Close() // SDK closes stdin, then terminates only this child if needed.
	if got := session.InitializeResult(); got.ServerInfo.Name != ServerName || got.ServerInfo.Version != ServerVersion || got.ProtocolVersion != ProtocolVersion {
		t.Fatal("production helper identity differs from the built-in catalog")
	}
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		return r
	}
	decode := func(r *mcp.CallToolResult, target any) {
		t.Helper()
		if r.IsError {
			message := "tool failed"
			for _, c := range r.Content {
				if text, ok := c.(*mcp.TextContent); ok {
					message = text.Text
					break
				}
			}
			t.Fatalf("fixture tool call: %s", message)
		}
		if r.StructuredContent == nil {
			t.Fatal("missing structured result")
		}
		data, err := json.Marshal(r.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, target); err != nil {
			t.Fatal(err)
		}
	}
	var list struct {
		Windows []Window `json:"windows"`
	}
	decode(call("computer_list_windows", map[string]any{}), &list)
	var target Window
	for _, w := range list.Windows {
		if w.PID == uint32(fixturePID) && w.Title == "Universal Code Computer Use Fixture" && w.HWND == initial.HWND {
			if target.WindowID != "" {
				t.Fatal("fixture window identity is ambiguous")
			}
			target = w
		}
	}
	if target.WindowID == "" {
		t.Fatal("own fixture window was not offered by the production helper")
	}
	// Drop the list without logging or persisting titles belonging to other apps.
	list.Windows = nil
	if path, err := ParseAppID(target.AppID); err != nil || !strings.EqualFold(filepath.Clean(path), filepath.Clean(fixture)) {
		t.Fatal("fixture application identity does not match its executable")
	}
	var observation Observation
	imageCount := 0
	observeResult := func(label string, r *mcp.CallToolResult) {
		t.Helper()
		var current Observation
		decode(r, &current)
		observation = current
		if observation.Window.WindowID != target.WindowID || observation.Window.AppID != target.AppID || observation.Window.PID != uint32(fixturePID) || observation.Window.HWND != initial.HWND || observation.ObservationID == "" {
			t.Fatal("observation no longer identifies our fixture")
		}
		var imageData []byte
		for _, c := range r.Content {
			if img, ok := c.(*mcp.ImageContent); ok {
				if imageData != nil || img.MIMEType != "image/png" {
					t.Fatal("unexpected image content")
				}
				imageData = img.Data
			}
		}
		config, err := png.DecodeConfig(bytes.NewReader(imageData))
		if err != nil {
			t.Fatalf("fixture PNG: %v", err)
		}
		if config.Width != observation.ImageWidth || config.Height != observation.ImageHeight || config.Width != observation.Window.Bounds.Width || config.Height != observation.Window.Bounds.Height {
			t.Fatal("PNG dimensions differ from observation coordinates")
		}
		imageCount++
		prefix := fmt.Sprintf("%02d-%s", imageCount, label)
		if err = os.WriteFile(filepath.Join(runDir, prefix+".png"), imageData, 0600); err != nil {
			t.Fatal(err)
		}
		meta, err := json.MarshalIndent(observation, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(runDir, prefix+".json"), meta, 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity := func() map[string]any { return map[string]any{"window_id": target.WindowID, "app_id": target.AppID} }
	action := func(label, name string, extra map[string]any) {
		t.Helper()
		args := identity()
		args["observation_id"] = observation.ObservationID
		for k, v := range extra {
			args[k] = v
		}
		observeResult(label, call(name, args))
	}
	clickControl := func(label string, rect fixtureRect) {
		t.Helper()
		x := (rect.Left+rect.Right)/2 - observation.Window.Bounds.X
		y := (rect.Top+rect.Bottom)/2 - observation.Window.Bounds.Y
		if x < 0 || y < 0 || x >= observation.ImageWidth || y >= observation.ImageHeight {
			t.Fatal("fixture control lies outside current observation")
		}
		action(label, "computer_click", map[string]any{"x": x, "y": y, "button": "left"})
	}
	activation := call("computer_activate", identity())
	activationMethod := "automatic"
	if activation.IsError && manualFocus {
		t.Log("Windows did not activate the fixture. Select the Universal Code Computer Use Fixture window; waiting up to 3 minutes without sending input.")
		deadline := time.Now().Add(3 * time.Minute)
		selected := false
		for time.Now().Before(deadline) && ctx.Err() == nil {
			var current struct {
				Windows []Window `json:"windows"`
			}
			decode(call("computer_list_windows", map[string]any{}), &current)
			for _, w := range current.Windows {
				// Clicking the title bar activates the window on mouse-down.
				// Wait for release before handing control to injected input.
				if w.WindowID == target.WindowID && w.PID == uint32(fixturePID) && w.HWND == initial.HWND && w.Foreground && modifiersClear() == nil {
					selected = true
				}
			}
			if selected {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !selected {
			t.Fatal("own fixture was not manually selected; no input verification was performed")
		}
		activationMethod = "manual_selection"
		activation = call("computer_observe", identity())
	}
	observeResult("initial-observation", activation)
	clickControl("focus-edit", initial.EditRect)
	// The classic multiline EDIT does not implement the application's usual
	// Ctrl+A shortcut. Select its full contents using its navigation keys.
	action("selection-start", "computer_key", map[string]any{"key": "CTRL+HOME"})
	action("select-all", "computer_key", map[string]any{"key": "CTRL+SHIFT+END"})
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("中文 English line %02d", i+1)
	}
	wantedText := strings.Join(lines, "\n")
	action("unicode-text", "computer_type", map[string]any{"text": wantedText})
	normalize := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n") }
	awaitState("exact Chinese and English text", func(s fixtureState) bool { return normalize(s.Text) == wantedText })
	action("scroll-start", "computer_key", map[string]any{"key": "CTRL+HOME"})
	before := awaitState("top of multiline text", func(s fixtureState) bool { return s.FirstVisibleLine == 0 })
	action("scroll-down", "computer_scroll", map[string]any{"dx": 0, "dy": -5})
	after := awaitState("multiline scroll movement", func(s fixtureState) bool { return s.FirstVisibleLine > before.FirstVisibleLine })
	stale := observation.ObservationID
	clickControl("button-click", after.ButtonRect)
	final := awaitState("one button click", func(s fixtureState) bool { return s.Clicks == 1 && normalize(s.Text) == wantedText })
	oldArgs := identity()
	oldArgs["observation_id"] = stale
	oldArgs["key"] = "ENTER"
	if r := call("computer_key", oldArgs); !r.IsError {
		t.Fatal("production helper accepted a consumed observation")
	}
	// Input can be queued before the control repaints. Retain a final image
	// after the fixture has acknowledged the exact text, scroll and click.
	observeResult("verified-final-state", call("computer_observe", identity()))
	if observation.Action != "" || observation.ActionSent {
		t.Fatal("pure observation was reported as an input action")
	}
	report := struct {
		FixturePID               int    `json:"fixture_pid"`
		ActivationMethod         string `json:"activation_method"`
		ImageCount               int    `json:"image_count"`
		UnicodeTextVerified      bool   `json:"unicode_text_verified"`
		ScrollFrom               int    `json:"scroll_from"`
		ScrollTo                 int    `json:"scroll_to"`
		ButtonClicks             int    `json:"button_clicks"`
		StaleObservationRejected bool   `json:"stale_observation_rejected"`
	}{fixturePID, activationMethod, imageCount, true, before.FirstVisibleLine, after.FirstVisibleLine, final.Clicks, true}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(runDir, "result.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Own fixture verified: Unicode text, scrolling %d→%d, %d button click, %d PNGs; artifacts: %s", before.FirstVisibleLine, after.FirstVisibleLine, final.Clicks, imageCount, runDir)
}
