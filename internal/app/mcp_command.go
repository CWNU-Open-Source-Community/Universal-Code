package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/mcpoauth"
	"cyberagent-workbench/internal/toolgateway"
)

const maxMCPDescriptorFileBytes = 128 * 1024

// mcpCommand owns both the existing local MCP Server and the new reviewed MCP
// Client control plane. Client credentials are always references; plaintext is
// accepted only from a named environment variable and is never printed.
func (a *App) mcpCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cyberagent mcp serve|client|credential")
	}
	if len(args) > 1 && args[0] == "client" && (args[1] == "inputs" || args[1] == "respond") {
		return a.mcpInputCommand(ctx, args[1:])
	}
	if err := a.ensureStore(); err != nil {
		return err
	}
	switch args[0] {
	case "serve":
		return a.mcpServeCommand(ctx, args[1:])
	case "client":
		return a.mcpClientCommand(ctx, args[1:])
	case "credential":
		return a.mcpCredentialCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown mcp subcommand %q", args[0])
	}
}

func (a *App) mcpServeCommand(ctx context.Context, args []string) error {
	fs := newFlagSet("mcp serve", a.errOut)
	runID := fs.String("run", "", "exact Run identity the MCP scope is bound to")
	workspaceID := fs.String("workspace", "", "exact Workspace identity the MCP scope is bound to")
	maxConcurrent := fs.Int("max-concurrent", 8, "maximum in-flight requests from 1 to 16")
	callTimeout := fs.Duration("call-timeout", 30*time.Second, "per-request timeout")
	sessionTTL := fs.Duration("session-ttl", 24*time.Hour, "capability TTL after initialize")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*runID) == "" || strings.TrimSpace(*workspaceID) == "" {
		return errors.New("usage: cyberagent mcp serve --run <run-id> --workspace <workspace-id>")
	}
	gateway := toolgateway.New(a.store, a.checker).
		WithWorkspaceRootResolver(func(ctx context.Context, workspaceID string) (string, error) {
			record, err := a.store.GetWorkspaceByID(ctx, workspaceID)
			return record.RootPath, err
		})
	server, err := mcp.New(mcp.Options{Store: a.store, Tools: gateway,
		RunID: *runID, WorkspaceID: *workspaceID, SessionTTL: *sessionTTL,
		CallTimeout: *callTimeout, MaxConcurrent: *maxConcurrent})
	if err != nil {
		return err
	}
	return server.Serve(ctx, os.Stdin, os.Stdout)
}

func (a *App) mcpClientCommand(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "inputs" || args[0] == "respond") {
		return a.mcpInputCommand(ctx, args)
	}
	if len(args) == 0 {
		return errors.New("usage: cyberagent mcp client stage|list|show|review|refresh|calls")
	}
	manager := a.newMCPClientManager()
	if manager == nil {
		return errors.New("MCP client runtime is unavailable")
	}
	if _, err := manager.ReconcileStartup(ctx); err != nil {
		return err
	}
	switch args[0] {
	case "stage":
		fs := newFlagSet("mcp client stage", a.errOut)
		file := fs.String("file", "", "bounded manual mcp-client.v1 or mcp-client.v3 descriptor JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
			return errors.New("usage: cyberagent mcp client stage --file <descriptor.json>")
		}
		var descriptor mcp.ServerDescriptor
		if err := decodeBoundedJSONFile(*file, maxMCPDescriptorFileBytes, &descriptor); err != nil {
			return err
		}
		record, replayed, err := manager.Stage(ctx, descriptor)
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, struct {
			Replayed bool             `json:"replayed"`
			Server   mcp.ServerRecord `json:"server"`
		}{Replayed: replayed, Server: record})
	case "list":
		fs := newFlagSet("mcp client list", a.errOut)
		workspaceID := fs.String("workspace", "", "exact Workspace identity")
		runID := fs.String("run", "", "optional exact Run identity")
		limit := fs.Int("limit", mcp.MaxClientServers, "maximum server records")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || strings.TrimSpace(*workspaceID) == "" {
			return errors.New("usage: cyberagent mcp client list --workspace <id> [--run <id>]")
		}
		values, err := a.store.ListMCPClientServers(ctx, *runID, *workspaceID, *limit)
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, values)
	case "show":
		if len(args) != 2 {
			return errors.New("usage: cyberagent mcp client show <server-id>")
		}
		value, err := a.store.GetMCPClientServer(ctx, args[1])
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "review":
		return a.mcpClientReviewCommand(ctx, manager, args[1:])
	case "refresh":
		if len(args) != 2 {
			return errors.New("usage: cyberagent mcp client refresh <server-id>")
		}
		value, err := manager.Refresh(ctx, args[1])
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, value)
	case "calls":
		fs := newFlagSet("mcp client calls", a.errOut)
		runID := fs.String("run", "", "exact Run identity")
		limit := fs.Int("limit", 100, "maximum metadata-only call receipts")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || strings.TrimSpace(*runID) == "" {
			return errors.New("usage: cyberagent mcp client calls --run <id> [--limit <n>]")
		}
		values, err := a.store.ListMCPClientCalls(ctx, *runID, *limit)
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, values)
	default:
		return fmt.Errorf("unknown mcp client subcommand %q", args[0])
	}
}

func (a *App) mcpClientReviewCommand(ctx context.Context, manager *mcp.Manager,
	args []string,
) error {
	fs := newFlagSet("mcp client review", a.errOut)
	action := fs.String("action", "", "approve_discovery|enable_capabilities|disable|revoke")
	descriptorFingerprint := fs.String("descriptor-fingerprint", "", "exact descriptor fingerprint")
	capabilityFingerprint := fs.String("capability-fingerprint", "", "exact discovered capability fingerprint")
	actor := fs.String("by", "", "review actor")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"action": true,
		"descriptor-fingerprint": true, "capability-fingerprint": true, "by": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 || *action == "" || *descriptorFingerprint == "" || *actor == "" {
		return errors.New("usage: cyberagent mcp client review <server-id> --action <action> --descriptor-fingerprint <sha256> [--capability-fingerprint <sha256>] --by <actor>")
	}
	value, err := manager.Review(ctx, fs.Arg(0), mcp.ReviewRequest{
		Action: mcp.ReviewAction(*action), ExpectedDescriptorFingerprint: *descriptorFingerprint,
		ExpectedCapabilityFingerprint: *capabilityFingerprint, ReviewedBy: *actor})
	if err != nil {
		return err
	}
	return writeExtensionJSON(a.out, value)
}

func (a *App) mcpCredentialCommand(ctx context.Context, args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "oauth-") {
		return a.mcpOAuthCommand(ctx, args)
	}
	if len(args) == 0 || a.credentials == nil {
		return errors.New("usage: cyberagent mcp credential status|set|delete <name>")
	}
	switch args[0] {
	case "status":
		if len(args) != 2 || !credential.ValidName(args[1]) {
			return errors.New("usage: cyberagent mcp credential status <name>")
		}
		configured, err := a.credentials.Configured(ctx, args[1])
		if err != nil {
			return err
		}
		return writeExtensionJSON(a.out, map[string]any{"name": args[1],
			"configured": configured, "store_kind": a.credentials.Kind(), "plaintext_returned": false})
	case "set":
		fs := newFlagSet("mcp credential set", a.errOut)
		fromEnvironment := fs.String("from-env", "", "environment variable containing the secret")
		confirm := fs.Bool("confirm", false, "confirm OS credential-store mutation")
		if err := fs.Parse(reorderFlags(args[1:], map[string]bool{"from-env": true, "confirm": false})); err != nil {
			return err
		}
		if fs.NArg() != 1 || !credential.ValidName(fs.Arg(0)) || !*confirm ||
			strings.TrimSpace(*fromEnvironment) == "" || !a.credentials.Available() {
			return errors.New("usage: cyberagent mcp credential set <name> --from-env <variable> --confirm")
		}
		secret := os.Getenv(*fromEnvironment)
		if len([]byte(secret)) < 8 || !credential.ValidSecret(secret) || mcpoauth.IsEnvelope(secret) {
			return errors.New("MCP credential environment value is missing or invalid")
		}
		unlock := mcpoauth.CredentialLock(fs.Arg(0))
		defer unlock()
		if err := a.credentials.Put(ctx, fs.Arg(0), secret); err != nil {
			return err
		}
		secret = ""
		return writeExtensionJSON(a.out, map[string]any{"name": fs.Arg(0),
			"configured": true, "store_kind": a.credentials.Kind(), "plaintext_returned": false})
	case "delete":
		fs := newFlagSet("mcp credential delete", a.errOut)
		confirm := fs.Bool("confirm", false, "confirm OS credential-store mutation")
		if err := fs.Parse(reorderFlags(args[1:], map[string]bool{"confirm": false})); err != nil {
			return err
		}
		if fs.NArg() != 1 || !credential.ValidName(fs.Arg(0)) || !*confirm || !a.credentials.Available() {
			return errors.New("usage: cyberagent mcp credential delete <name> --confirm")
		}
		unlock := mcpoauth.CredentialLock(fs.Arg(0))
		defer unlock()
		if err := a.credentials.Delete(ctx, fs.Arg(0)); err != nil {
			return err
		}
		return writeExtensionJSON(a.out, map[string]any{"name": fs.Arg(0),
			"configured": false, "store_kind": a.credentials.Kind(), "plaintext_returned": false})
	default:
		return fmt.Errorf("unknown mcp credential subcommand %q", args[0])
	}
}

func decodeBoundedJSONFile(path string, maxBytes int64, target any) error {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > maxBytes {
		return errors.New("JSON input exceeds its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON input contains trailing data")
	}
	return nil
}

func writeExtensionJSON(destination io.Writer, value any) error {
	encoder := json.NewEncoder(destination)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// The CLI owns the callback lifetime; Ctrl+C cancels it without persisting tokens.
func (a *App) mcpOAuthCommand(ctx context.Context, args []string) error {
	fs := newFlagSet("mcp credential OAuth", a.errOut)
	issuer := fs.String("issuer", "", "explicitly trusted discovered HTTPS issuer")
	clientID := fs.String("client-id", "", "OAuth client ID or hosted metadata URL")
	clientIDMode := fs.String("client-id-mode", "", "auto, dynamic, pre_registered (default), or metadata_url")
	clientSecretEnv := fs.String("client-secret-env", "", "environment variable containing the confidential client secret")
	callbackPort := fs.Int("callback-port", 0, "loopback callback port; 0 chooses an available port")
	confirm := fs.Bool("confirm", false, "confirm OAuth metadata network access and credential change")
	if err := fs.Parse(reorderFlags(args[1:], map[string]bool{"issuer": true, "client-id": true, "client-id-mode": true, "client-secret-env": true, "callback-port": true, "confirm": false})); err != nil {
		return err
	}
	if fs.NArg() != 1 || (args[0] != "oauth-status" && !*confirm) {
		return errors.New("usage: cyberagent mcp credential oauth-discover|oauth-login|oauth-status <server-id> --confirm [--issuer <url> --client-id <id>]")
	}
	record, err := a.store.GetMCPClientServer(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	d := record.Descriptor
	binding := application.MCPCredentialBinding{ServerID: d.ID, WorkspaceID: d.WorkspaceID, RunID: d.RunID, ExpectedDescriptorFingerprint: record.DescriptorFingerprint, Target: d.Target, CredentialRef: d.CredentialRef}
	service := application.NewMCPCredentialService(a.store, a.credentials)
	status, err := service.Status(ctx, binding)
	if err != nil {
		return err
	}
	if args[0] == "oauth-status" {
		return writeExtensionJSON(a.out, status)
	}
	action := "oauth-discover"
	if args[0] == "oauth-login" {
		action = "oauth-start"
	} else if args[0] != "oauth-discover" {
		return errors.New("unknown MCP OAuth command")
	}
	request := application.ChangeMCPCredentialRequest{Binding: binding, Version: application.MCPCredentialProtocolVersion, Action: action, Confirm: *confirm, ExpectedReferenceFingerprint: status.ReferenceFingerprint, OAuthIssuer: *issuer, OAuthClientID: *clientID, OAuthClientIDMode: *clientIDMode, OAuthCallbackPort: *callbackPort}
	if *clientSecretEnv != "" {
		if action != "oauth-start" {
			return errors.New("client-secret-env is only valid for oauth-login")
		}
		request.OAuthClientSecret = os.Getenv(*clientSecretEnv)
		if request.OAuthClientSecret == "" {
			return errors.New("OAuth client secret environment variable is empty")
		}
	}
	status, err = service.Change(ctx, request)
	if err != nil {
		return err
	}
	if action == "oauth-start" {
		cancellation := request
		cancellation.Action = "oauth-cancel"
		cancellation.OAuthFlowID = status.OAuth.FlowID
		cancellation.OAuthIssuer = ""
		cancellation.OAuthClientID = ""
		cancellation.OAuthClientIDMode = ""
		cancellation.OAuthClientSecret = ""
		cancellation.OAuthCallbackPort = 0
		defer func() { _, _ = service.Change(context.Background(), cancellation) }()
	}
	if err = writeExtensionJSON(a.out, status); err != nil {
		return err
	}
	if action != "oauth-start" {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err = service.Status(ctx, binding)
			if err != nil {
				return err
			}
			if status.OAuth == nil {
				return errors.New("MCP OAuth state is unavailable")
			}
			if status.OAuth.State == "authorizing" || status.OAuth.State == "starting" {
				continue
			}
			if err = writeExtensionJSON(a.out, status); err != nil {
				return err
			}
			if status.OAuth.State != "authorized" {
				return errors.New("MCP OAuth authorization did not complete")
			}
			return nil
		}
	}
}
