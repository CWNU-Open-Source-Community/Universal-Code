//go:build darwin && cgo

package browserruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// This is an opt-in native experiment, not a product support advertisement.
// CI explicitly requests it, so missing Chrome and native failures are fatal.
func TestDarwinChromeNativeBindingAndLifecycle(t *testing.T) {
	if os.Getenv("TRAVERSE_TEST_DARWIN_CHROME") != "1" {
		t.Skip("native signed Chrome experiment is opt-in")
	}
	const bundle = "/Applications/Google Chrome.app"
	const executable = bundle + "/Contents/MacOS/Google Chrome"
	hash, err := darwinChromeCDHash(bundle)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixed publisher policy accepted; native CDHash=%s", hash)
	profile := darwinChromeTestProfile(t)
	// The real signed executable must not resume when the approved hash differs.
	badHash := strings.Repeat("0", len(hash))
	if _, err = darwinChromeSpawn(executable, profile, badHash); !darwinChromeRequirementRejected(err) {
		t.Fatalf("wrong hash must fail the dynamic code requirement, got %v", err)
	}
	if _, err = os.Stat(filepath.Join(profile, "Data", DevToolsActivePortFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused child published a CDP endpoint: %v", err)
	}
	t.Log("wrong CDHash rejected while child remained suspended; child reaped")

	var reports []map[string]any
	for _, mode := range []string{"browser_close", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			report := darwinChromeTestSession(t, executable, hash, mode)
			reports = append(reports, report)
		})
	}
	if output := os.Getenv("TRAVERSE_DARWIN_CHROME_EVIDENCE"); output != "" {
		if err := os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		content, err := json.MarshalIndent(map[string]any{"publisher_policy": "google_chrome_stable_developer_id", "cdhash": hash,
			"native_success": !t.Failed(), "dynamic_identity_before_resume": len(reports) == 2 && !t.Failed(), "wrong_hash_refused": true, "sessions": reports,
			"product_enabled": false, "tree_reaped_claimed": false, "job_hard_limits_claimed": false}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(output, "native-experiment.json"), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func darwinChromeTestProfile(t *testing.T) string {
	t.Helper()
	// macOS /var is a link. Pass the actual canonical temp directory to Chrome.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range []string{"Data", "Home", "Temp", "Crashpad"} {
		if err = os.Mkdir(filepath.Join(root, child), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func darwinChromeTestSession(t *testing.T, executable, hash, mode string) map[string]any {
	t.Helper()
	profile := darwinChromeTestProfile(t)
	pid, err := darwinChromeSpawn(executable, profile, hash)
	if err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			code, waitErr := darwinChromeWait(pid)
			t.Logf("failure cleanup root=%d exit=%d wait=%v", pid, code, waitErr)
		}
		if t.Failed() {
			if b, readErr := os.ReadFile(filepath.Join(profile, "browser.stderr.log")); readErr == nil {
				if len(b) > 65536 {
					b = b[:65536]
				}
				t.Logf("owned Chrome stderr: %s", b)
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var endpoint string
	for endpoint == "" {
		url, pending, readErr := readDevToolsEndpoint(filepath.Join(profile, "Data"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !pending {
			endpoint = url.String()
			break
		}
		exited, exitErr := darwinChromeExited(pid)
		if exitErr != nil || exited {
			t.Fatalf("Chrome exited before CDP: exited=%t err=%v", exited, exitErr)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	connection, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetReadLimit(24 * 1024 * 1024)
	sequence := 0
	call := func(method, session string, params any, out any) error {
		sequence++
		if deadline, ok := ctx.Deadline(); ok {
			_ = connection.SetReadDeadline(deadline)
			_ = connection.SetWriteDeadline(deadline)
		}
		request := map[string]any{"id": sequence, "method": method, "params": params}
		if session != "" {
			request["sessionId"] = session
		}
		if err := connection.WriteJSON(request); err != nil {
			return err
		}
		for {
			var message struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := connection.ReadJSON(&message); err != nil {
				return err
			}
			if message.ID != sequence {
				continue
			}
			if len(message.Error) != 0 {
				return fmt.Errorf("%s: %s", method, message.Error)
			}
			if out == nil {
				return nil
			}
			return json.Unmarshal(message.Result, out)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Native Mac browser experiment</title><style>body{font:24px system-ui;margin:64px;background:#f5f7fa;color:#142234}input,button{font:inherit;padding:12px;margin:8px 0}h1{font-size:36px}</style><h1>Native Mac browser experiment</h1><p>Signed Chrome · dedicated temporary profile</p><label>Message <input id="message" aria-label="Message"></label><button id="apply" onclick="document.getElementById('result').textContent='Applied: '+document.getElementById('message').value">Apply</button><p id="result">Draft ready</p>`)
	}))
	defer server.Close()
	var target struct {
		ID string `json:"targetId"`
	}
	if err = call("Target.createTarget", "", map[string]any{"url": "about:blank"}, &target); err != nil {
		t.Fatal(err)
	}
	var attached struct {
		ID string `json:"sessionId"`
	}
	if err = call("Target.attachToTarget", "", map[string]any{"targetId": target.ID, "flatten": true}, &attached); err != nil {
		t.Fatal(err)
	}
	if err = call("Page.enable", attached.ID, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err = call("Page.navigate", attached.ID, map[string]any{"url": server.URL}, nil); err != nil {
		t.Fatal(err)
	}
	evaluate := func(expression string) (string, error) {
		var result struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		err := call("Runtime.evaluate", attached.ID, map[string]any{"expression": expression, "returnByValue": true}, &result)
		return result.Result.Value, err
	}
	for {
		text, evaluateErr := evaluate("document.body ? document.body.innerText : ''")
		if evaluateErr != nil {
			t.Fatal(evaluateErr)
		}
		if strings.Contains(text, "Draft ready") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	screenshot := func(name string) {
		t.Helper()
		var result struct {
			Data string `json:"data"`
		}
		if err := call("Page.captureScreenshot", attached.ID, map[string]any{"format": "png"}, &result); err != nil {
			t.Fatal(err)
		}
		png, err := base64.StdEncoding.Strict().DecodeString(result.Data)
		if err != nil || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatalf("real browser PNG: %v", err)
		}
		if output := os.Getenv("TRAVERSE_DARWIN_CHROME_EVIDENCE"); output != "" {
			if err = os.MkdirAll(output, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(output, mode+"-"+name+".png"), png, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	screenshot("before-interaction")
	if _, err = evaluate("document.getElementById('message').focus(); 'focused'"); err != nil {
		t.Fatal(err)
	}
	if err = call("Input.insertText", attached.ID, map[string]any{"text": "Native input verified"}, nil); err != nil {
		t.Fatal(err)
	}
	var box struct {
		Result struct {
			Value struct{ X, Y float64 } `json:"value"`
		} `json:"result"`
	}
	if err = call("Runtime.evaluate", attached.ID, map[string]any{"expression": "(()=>{const r=document.getElementById('apply').getBoundingClientRect();return {X:r.x+r.width/2,Y:r.y+r.height/2}})()", "returnByValue": true}, &box); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		if err = call("Input.dispatchMouseEvent", attached.ID, map[string]any{"type": kind, "x": box.Result.Value.X, "y": box.Result.Value.Y, "button": "left", "clickCount": 1}, nil); err != nil {
			t.Fatal(err)
		}
	}
	text, err := evaluate("document.getElementById('result').textContent")
	if err != nil || text != "Applied: Native input verified" {
		t.Fatalf("actual click/input effect=%q err=%v", text, err)
	}
	screenshot("after-interaction")
	group, err := darwinChromeGroup(pid)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := darwinChromeCrashpad(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(handlers) == 0 {
		t.Fatal("running Chrome has no observable handler bound to its private Crashpad database; lifecycle scope is unproven")
	}
	t.Logf("root=%d PGID members=%v scoped Crashpad=%v", pid, group, handlers)
	startedStop := time.Now()
	if mode == "browser_close" {
		// Browser.close may close the socket before replying. Exit is checked below.
		_ = call("Browser.close", "", map[string]any{}, nil)
	} else if err = syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	var lastGroup, lastHandlers []int
	deadline := time.Now().Add(20 * time.Second)
	for {
		exited, exitErr := darwinChromeExited(pid)
		if exitErr != nil {
			t.Fatal(exitErr)
		}
		members, groupErr := darwinChromeGroup(pid)
		if groupErr != nil {
			t.Fatal(groupErr)
		}
		lastGroup = nil
		for _, member := range members {
			sample, sampleErr := darwinChromeSample(member)
			if errors.Is(sampleErr, syscall.ESRCH) {
				continue
			}
			if sampleErr != nil {
				t.Fatal(sampleErr)
			}
			if !sample.Zombie {
				lastGroup = append(lastGroup, member)
			}
		}
		lastHandlers, err = darwinChromeCrashpad(profile)
		if err != nil {
			t.Fatal(err)
		}
		if exited && len(lastGroup) == 0 && len(lastHandlers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup unresolved rootExited=%t group=%v scopedCrashpad=%v", exited, lastGroup, lastHandlers)
		}
		time.Sleep(100 * time.Millisecond)
	}
	code, err := darwinChromeWait(pid)
	reaped = err == nil
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(startedStop)
	t.Logf("root exit=%d; observed process group/own Crashpad empty after %s; no Job/tree guarantee claimed", code, elapsed)
	return map[string]any{"mode": mode, "pid": pid, "members_before_close": group, "crashpad_before_close": handlers, "exit_code": code,
		"root_reaped": true, "process_group_empty": true, "scoped_crashpad_empty": true, "cleanup_milliseconds": elapsed.Milliseconds(), "navigation_interaction_screenshots": true}
}
