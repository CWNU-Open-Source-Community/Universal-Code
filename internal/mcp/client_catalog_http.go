package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// The SDK's private sessionUpdated hook cannot cross our response observer.
// This single legacy GET carries only catalog notifications for the exact
// initialized HTTP session. Modern sessions use SDK subscriptions/listen.
func (c *Client) startCatalogNotifications(ctx context.Context, check func(context.Context) error) error {
	kind := c.descriptor.Transport
	if c.resolved != nil {
		kind = TransportKind(c.resolved.launch.Transport)
	}
	if kind != TransportStreamableHTTP {
		return nil
	}
	if c.remote == nil || c.catalogListening || c.session == nil || c.session.InitializeResult().ProtocolVersion == preferredClientProtocolVersion {
		return nil
	}
	if check == nil {
		return errors.New("MCP notification stream requires current task authority")
	}
	if err := check(ctx); err != nil {
		return runtimeAuthorityFailure(err)
	}
	id, _ := c.remote.sessionID.Load().(string)
	if id == "" {
		return nil
	} // A stateless legacy peer has no standalone session.
	listenCtx, cancel := context.WithCancel(c.lifetime)
	stop := context.AfterFunc(ctx, cancel)
	request, err := http.NewRequestWithContext(listenCtx, http.MethodGet, c.remote.origin.String(), nil)
	if err != nil {
		stop()
		cancel()
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Mcp-Session-Id", id)
	// RoundTrip retains the configured origin/header/credential protections. This
	// request is created only here, once, while holding the current task lease.
	response, err := c.remote.RoundTrip(request)
	stop()
	if err != nil {
		cancel()
		return err
	}
	if response.StatusCode == http.StatusMethodNotAllowed {
		response.Body.Close()
		cancel()
		c.catalogListening = true
		return nil
	}
	media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || media != "text/event-stream" {
		response.Body.Close()
		cancel()
		return errors.New("MCP catalog notification stream was rejected")
	}
	if err := check(ctx); err != nil {
		response.Body.Close()
		cancel()
		return runtimeAuthorityFailure(err)
	}
	c.catalogListening = true
	go func() {
		defer cancel()
		defer response.Body.Close()
		err := c.readCatalogNotifications(response.Body)
		if c.lifetime.Err() == nil && c.catalogError != nil {
			c.catalogError(err)
		}
	}()
	return nil
}

func (c *Client) readCatalogNotifications(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	var data []byte
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) == 0 {
				continue
			}
			message, err := jsonrpc.DecodeMessage(bytes.TrimSpace(data))
			data = nil
			if err != nil {
				return errors.New("invalid MCP catalog notification")
			}
			request, ok := message.(*jsonrpc.Request)
			if !ok || request.IsCall() {
				return errors.New("MCP catalog stream received an unexpected request")
			}
			switch request.Method {
			case "notifications/tools/list_changed", "notifications/resources/list_changed", "notifications/prompts/list_changed":
				c.invalidateCatalog()
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if len(data)+len(value)+1 > MaxMessageBytes {
				return errors.New("MCP catalog notification exceeds the event limit")
			}
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
