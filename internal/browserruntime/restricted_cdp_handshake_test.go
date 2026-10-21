package browserruntime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRestrictedCDPHandshakeUsesRemainingStartupBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(RestrictedCDPOperationTimeout + 200*time.Millisecond):
		case <-r.Context().Done():
			return
		}
		upgrader := websocket.Upgrader{}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_, _, _ = connection.ReadMessage()
	}))
	defer server.Close()
	endpoint, _ := url.Parse("ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/owned")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	connection, err := dialRestrictedCDP(ctx, endpoint)
	if err != nil {
		t.Fatalf("handshake inside startup deadline was rejected: %v", err)
	}
	defer connection.Close()
	// Canceling the completed handshake must not close the live CDP session.
	cancel()
	if err := connection.WriteMessage(websocket.TextMessage, []byte("ready")); err != nil {
		t.Fatalf("completed handshake retained cancellation hook: %v", err)
	}
}

func TestRestrictedCDPHandshakeStopsOnCancellationOrDeadline(t *testing.T) {
	for _, earlyCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancellation"}[earlyCancel], func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			endpoint, _ := url.Parse("ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/owned")
			budget := 100 * time.Millisecond
			if earlyCancel {
				budget = time.Minute
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			if earlyCancel {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			started := time.Now()
			connection, err := dialRestrictedCDP(ctx, endpoint)
			if connection != nil {
				connection.Close()
				t.Fatal("unfinished handshake returned a connection")
			}
			var timeout net.Error
			if earlyCancel && !errors.Is(err, context.Canceled) ||
				!earlyCancel && !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
				t.Fatalf("handshake lost its cancellation/timeout cause: %v", err)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("canceled handshake waited for startup budget")
			}
		})
	}
}
