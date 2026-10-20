//go:build workbenchjourney

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"cyberagent-workbench/internal/browserruntime"
)

var browserJourneyURL string

// This single-journey offline provider remembers only results it actually
// received. The supervisor starts a new bounded batch with fresh tool history;
// preserving observed progress lets the fixture continue the same real page.
var browserJourneyProgress struct {
	sync.Mutex
	seen      map[string]bool
	completed int
	snapshot  browserruntime.AgentBrowserSnapshot
}

const browserJourneyValue = "Native browser verified"

func newBrowserJourneySite() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html lang="en"><meta charset="utf-8"><title>Owned Agent Browser check</title>
<style>body{margin:48px;font:22px system-ui;color:#172632;background:#f7faf9}main{max-width:880px}input,button{font:inherit;padding:12px;margin:12px 8px 12px 0}h1{font-size:36px}#result{padding:24px;background:#e2f3e9;border-radius:12px}</style>
<main><h1>Owned Agent Browser check</h1><p>This local page is controlled by the acceptance test.</p><label>Query <input aria-label="Query"></label><button onclick="document.querySelector('#result').textContent='Applied: '+document.querySelector('input').value">Apply locally</button><p id="result">Waiting for browser input</p></main></html>`)
	}))
}

func nextBrowserJourneyResponse(request modelRequest, results []contentBlock,
	tool func(string, any) (contentBlock, error), text func(any) (contentBlock, error)) (contentBlock, error) {
	wait := func(message string) (contentBlock, error) {
		return text(map[string]string{"version": "root_lifecycle.v1", "action": "wait", "message": message, "reason": "Operator is inspecting the actual browser page"})
	}
	available := false
	for _, offered := range request.Tools {
		available = available || offered.Name == "browser_navigate"
	}
	if browserJourneyURL == "" {
		return contentBlock{}, errors.New("browser journey requires its owned local fixture")
	}
	type envelope struct {
		Tool   string `json:"tool"`
		Stdout string `json:"stdout"`
	}
	browserJourneyProgress.Lock()
	defer browserJourneyProgress.Unlock()
	if browserJourneyProgress.seen == nil {
		browserJourneyProgress.seen = map[string]bool{}
	}
	for _, result := range results {
		var encoded string
		if json.Unmarshal(result.Content, &encoded) != nil {
			continue
		}
		var value envelope
		if json.Unmarshal([]byte(encoded), &value) != nil || !strings.HasPrefix(value.Tool, "browser_") {
			continue
		}
		if result.IsError {
			return contentBlock{}, fmt.Errorf("actual browser tool %s failed: %s", value.Tool, encoded)
		}
		if result.ToolUseID == "" {
			return contentBlock{}, errors.New("actual browser result has no tool call identity")
		}
		if browserJourneyProgress.seen[result.ToolUseID] {
			continue
		}
		browserJourneyProgress.seen[result.ToolUseID] = true
		browserJourneyProgress.completed++
		if value.Tool == "browser_snapshot" {
			if err := json.Unmarshal([]byte(value.Stdout), &browserJourneyProgress.snapshot); err != nil {
				return contentBlock{}, err
			}
		}
	}
	snapshot := browserJourneyProgress.snapshot
	completed := browserJourneyProgress.completed
	selectElement := func(name string) (string, error) {
		for _, element := range snapshot.Elements {
			if element.Name == name && element.Ref != "" && snapshot.SnapshotID != "" {
				return element.Ref, nil
			}
		}
		return "", fmt.Errorf("actual browser snapshot did not expose %q", name)
	}
	fmt.Fprintf(os.Stderr, "browser journey completed=%d snapshot=%t\n", completed, snapshot.SnapshotID != "")
	if !available && completed == 0 {
		return wait("JOURNEY_BROWSER_UNAVAILABLE: 当前运行未提供浏览器工具，未执行页面验收。")
	}
	if !available && completed < 7 {
		// The real supervisor closes its native tool batch after four rounds.
		// Continue at that scheduling boundary instead of treating it as a host
		// capability change or bypassing the bounded tool-loop policy.
		return text(map[string]string{"version": "root_lifecycle.v1", "action": "continue", "message": "继续检查已打开的本地浏览器页面。"})
	}
	switch completed {
	case 0:
		return tool("browser_navigate", map[string]any{"version": "browser_navigate.v2", "url": browserJourneyURL})
	case 1, 3, 5:
		return tool("browser_snapshot", map[string]string{"version": "browser_snapshot.v2"})
	case 2:
		ref, err := selectElement("Query")
		if err != nil {
			return contentBlock{}, err
		}
		return tool("browser_type", map[string]string{"version": "browser_type.v2", "snapshot_id": snapshot.SnapshotID, "element_ref": ref, "value": browserJourneyValue, "mode": "replace"})
	case 4:
		ref, err := selectElement("Apply locally")
		if err != nil {
			return contentBlock{}, err
		}
		return tool("browser_click", map[string]string{"version": "browser_click.v2", "snapshot_id": snapshot.SnapshotID, "element_ref": ref})
	case 6:
		if !strings.Contains(snapshot.Text, "Applied: "+browserJourneyValue) {
			return contentBlock{}, errors.New("actual local input/click result was not observed")
		}
		return tool("browser_screenshot", map[string]string{"version": "browser_screenshot.v2"})
	case 7:
		return contentBlock{Type: "journey_browser_hold"}, nil
	default:
		return contentBlock{}, errors.New("unexpected repeated browser journey continuation")
	}
}
