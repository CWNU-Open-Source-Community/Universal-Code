package mcp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/toolcontract"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func isClientInvocationMethod(method string) bool {
	return method == "tools/call" || method == "resources/read" || method == "prompts/get"
}

// FingerprintContentInput includes the method: equal JSON on two operations
// cannot reuse authorization. SDK-owned protocol metadata grants no authority.
func FingerprintContentInput(key []byte, launch toolcontract.ResolvedLaunch, method string, params json.RawMessage) (string, error) {
	if method != "resources/read" && method != "prompts/get" {
		return "", errors.New("invalid MCP content method")
	}
	if len(params) == 0 || len(params) > MaxClientArgumentsBytes || !json.Valid(params) {
		return "", errors.New("invalid MCP content parameters")
	}
	var args map[string]any
	d := json.NewDecoder(bytes.NewReader(params))
	d.UseNumber()
	if err := d.Decode(&args); err != nil || args == nil {
		return "", errors.New("invalid MCP content parameters")
	}
	delete(args, "_meta")
	launchFP, err := toolcontract.FingerprintLaunch(key, launch)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Launch, Method string
		Params         any
	}{launchFP, method, args})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mcp.content.input.v1\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// InvokeContentWithReceipt shares the tool call's dispatch guard, cancellation
// identity and no-replay attempt. Prompt roles/content remain untrusted data.
func (c *Client) InvokeContentWithReceipt(ctx context.Context, operation toolcontract.Operation, method string, params json.RawMessage) (json.RawMessage, toolcontract.Receipt, error) {
	receipt := toolcontract.Receipt{OperationID: operation.ID, State: toolcontract.ReceiptNotDispatched}
	if c == nil || c.resolved == nil || c.closed.Load() || operation.Validate() != nil || operation.Kind != toolcontract.OperationToolCall || operation.Component != c.resolved.launch.Component || operation.CapabilityFingerprint != c.NativeCapabilityFingerprint() {
		return nil, receipt, errors.New("MCP content operation is not bound to discovery")
	}
	input, err := FingerprintContentInput(c.resolved.key, c.resolved.launch, method, params)
	if err != nil || input != operation.InputFingerprint {
		return nil, receipt, errors.New("MCP content input differs from its operation")
	}
	attempt, capture := &sdkCallAttempt{}, &sdkRawResponse{}
	ctx = context.WithValue(ctx, resolvedCallKey{}, &resolvedCall{operation: cloneOperation(operation)})
	ctx = context.WithValue(ctx, sdkCallAttemptKey{}, attempt)
	ctx = context.WithValue(ctx, sdkRawResponseKey{}, capture)
	session, err := c.connect(ctx)
	if err != nil {
		return nil, receipt, c.sdkError(err)
	}
	switch method {
	case "resources/read":
		var p sdk.ReadResourceParams
		if json.Unmarshal(params, &p) != nil {
			return nil, receipt, errors.New("invalid resource parameters")
		}
		p.Meta = c.callProgressMeta(operation.ID)
		_, err = session.ReadResource(ctx, &p)
	case "prompts/get":
		var p sdk.GetPromptParams
		if json.Unmarshal(params, &p) != nil {
			return nil, receipt, errors.New("invalid prompt parameters")
		}
		p.Meta = c.callProgressMeta(operation.ID)
		_, err = session.GetPrompt(ctx, &p)
	default:
		return nil, receipt, errors.New("invalid MCP content method")
	}
	raw, wireError := capture.result()
	if len(raw) > 0 || wireError != nil {
		receipt.State = toolcontract.ReceiptResultReceived
	} else if attempt.dispatched.Load() {
		receipt.State = toolcontract.ReceiptOutcomeUnknown
	}
	if wireError != nil {
		receipt.ErrorCode = "remote_rpc_error"
		return nil, receipt, errors.New("MCP peer returned a JSON-RPC error")
	}
	if len(raw) > 0 {
		var shape map[string]json.RawMessage
		if len(raw) > MaxMessageBytes || json.Unmarshal(raw, &shape) != nil {
			return raw, receipt, errors.New("invalid MCP content result")
		}
		if string(shape["resultType"]) == `"input_required"` {
			return raw, receipt, &sdkInputRequiredError{raw: raw}
		}
		field := "contents"
		if method == "prompts/get" {
			field = "messages"
		}
		value := bytes.TrimSpace(shape[field])
		if len(value) == 0 || value[0] != '[' {
			return raw, receipt, errors.New("missing MCP content result")
		}
	}
	if receipt.State == toolcontract.ReceiptNotDispatched {
		attempt.mu.Lock()
		rejection := attempt.rejection
		attempt.mu.Unlock()
		if rejection != nil {
			err = rejection
		}
	}
	if err != nil {
		receipt.ErrorCode = "mcp_call_failed"
		return raw, receipt, c.sdkError(err)
	}
	if len(raw) == 0 {
		return nil, receipt, errors.New("missing MCP content response")
	}
	return raw, receipt, nil
}
