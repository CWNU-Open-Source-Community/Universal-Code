package runner

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
)

// CommandServiceReadiness is a point-in-time observation, not a network grant
// or an assertion that the application's behavior is correct.
type CommandServiceReadiness struct {
	JobID      string    `json:"job_id"`
	URL        string    `json:"url"`
	Source     string    `json:"source"`
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Verified   bool      `json:"verified"`
	CheckedAt  time.Time `json:"checked_at"`
}

type commandServiceListenerOwner interface {
	OwnsTCPListener(net.IP, uint16) bool
}

// CheckOwnedCommandService only probes an address emitted by this live Job.
// The caller must supply its current authorization check. Neither persisted
// PIDs nor a printed URL can authorize contacting another local service.
func (m *CommandRuntimeManager) CheckOwnedCommandService(ctx context.Context, id CommandRuntimeJobIdentity, rawURL string, authorize func(context.Context) error) (CommandServiceReadiness, error) {
	result := CommandServiceReadiness{JobID: id.ID, URL: rawURL, Source: "command_output", State: "unavailable", CheckedAt: time.Now().UTC()}
	canonical, ok := CanonicalCommandServiceURL(rawURL)
	if !ok || canonical != rawURL || authorize == nil || m == nil || ctx == nil {
		return result, ErrCommandRuntimeBoundary
	}
	if err := authorize(ctx); err != nil {
		return result, err
	}
	if id.Adapter.Kind != commandruntimeadapter.KindHostUnsandboxed {
		result.Reason = "backend_not_supported"
		return result, nil
	}
	entry := m.entry(id.ID)
	if entry == nil {
		result.Reason = "job_not_owned"
		return result, nil
	}
	entry.mu.Lock()
	job, process := entry.record, entry.process
	entry.mu.Unlock()
	if id != CommandRuntimeIdentity(job) || !m.OwnsActiveJob(job) || job.Network != CommandRuntimeNetworkHost {
		return result, ErrCommandRuntimeBoundary
	}
	candidate := false
	for _, value := range CommandServiceURLCandidates([]CommandRuntimeFrame{{Text: job.Stdout}, {Text: job.Stderr}}) {
		candidate = candidate || value.URL == canonical
	}
	if !candidate {
		result.Reason = "address_not_in_job_output"
		return result, nil
	}
	owner, ok := process.(commandServiceListenerOwner)
	if !ok {
		result.Reason = "listener_ownership_unavailable"
		return result, nil
	}
	parsed, _ := url.Parse(canonical)
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" {
			port = "443"
		}
	}
	address := net.JoinHostPort(parsed.Hostname(), port)
	endpoint, err := net.ResolveTCPAddr("tcp", address)
	if err != nil || !endpoint.IP.IsLoopback() {
		return result, ErrCommandRuntimeBoundary
	}
	owned := func() bool { return m.OwnsActiveJob(job) && owner.OwnsTCPListener(endpoint.IP, uint16(endpoint.Port)) }
	result.State = "not_ready"
	if !owned() {
		result.Reason = "listener_not_owned"
		return result, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true,
		DialContext: func(c context.Context, network, target string) (net.Conn, error) {
			if target != address || authorize(c) != nil || !owned() {
				return nil, ErrCommandRuntimeBoundary
			}
			return (&net.Dialer{}).DialContext(c, "tcp", address)
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, canonical, nil)
	response, probeErr := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
		result.HTTPStatus = response.StatusCode
	}
	result.CheckedAt = time.Now().UTC()
	if err := authorize(ctx); err != nil {
		return result, err
	}
	if !owned() {
		result.Reason = "listener_not_owned"
		return result, nil
	}
	if probeErr != nil {
		result.Reason = "connection_failed"
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			result.Reason = "timeout"
		}
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Reason = "http_not_ready"
		return result, nil
	}
	result.State, result.Verified = "ready", true
	return result, nil
}
