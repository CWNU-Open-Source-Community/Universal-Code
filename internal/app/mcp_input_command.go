package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"cyberagent-workbench/internal/mcp"
)

// The CLI talks to the live host; a second process cannot recover pending
// input from SQLite. Answers are read from stdin and never printed.
func (a *App) mcpInputCommand(ctx context.Context, args []string) error {
	fs := newFlagSet("mcp client "+args[0], a.errOut)
	endpoint := fs.String("api", "http://127.0.0.1:8765", "live loopback API URL")
	runID := fs.String("run", "", "active Run identity")
	inputID := fs.String("input", "", "pending input identity (respond)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *runID == "" || strings.ContainsAny(*runID, "/?#\\") || (args[0] == "respond" && (*inputID == "" || strings.ContainsAny(*inputID, "/?#\\"))) {
		return errors.New("usage: mcp client inputs|respond --run <id> [--input <id>] [--api <loopback-url>]; respond reads JSON from stdin")
	}
	u, err := url.Parse(*endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Hostname() != "localhost" && (net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback())) {
		return errors.New("MCP input CLI requires a loopback API URL")
	}
	u.Path = "/api/v1/runs/" + *runID + "/mcp-inputs"
	method, token := http.MethodGet, os.Getenv(apiTokenEnvironment)
	var body []byte
	if args[0] == "respond" {
		u.Path += "/" + *inputID
		method = http.MethodPost
		token = os.Getenv(apiControlTokenEnvironment)
		body, err = io.ReadAll(io.LimitReader(os.Stdin, 36*1024+1))
		if err != nil || len(body) > 36*1024 {
			return errors.New("MCP input response exceeds its bound")
		}
		var response mcp.InputResponse
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&response) != nil || response.Version != mcp.InputProtocolVersion {
			return errors.New("invalid MCP input response JSON")
		}
	}
	if token == "" {
		return errors.New("set the live API access/control token environment variable")
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid live API request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("live MCP input API is unreachable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if err != nil || len(raw) > 512*1024 {
		return errors.New("live MCP input API response exceeds its bound")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("live MCP input API rejected the request (HTTP %d)", resp.StatusCode)
	}
	if method == http.MethodPost {
		var result struct {
			Data struct {
				Accepted bool `json:"accepted"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &result) != nil || !result.Data.Accepted {
			return errors.New("live API did not confirm the input response")
		}
		_, err = fmt.Fprintln(a.out, "MCP input response delivered")
		return err
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 {
		return errors.New("invalid live MCP input API response")
	}
	_, err = fmt.Fprintln(a.out, string(envelope.Data))
	return err
}
