//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

func TestDesktopBindingErrorsEncodeReadableMessages(t *testing.T) {
	const sizeMessage = "File actions support files up to 5 MiB"
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"native error", errors.New("clipboard is busy; try copying again"), "clipboard is busy; try copying again"},
		{"application error", apperror.New(apperror.CodeResourceExhausted, sizeMessage), sizeMessage},
		{"wrapped application error", fmt.Errorf("private outer context: %w", apperror.Wrap(apperror.CodeResourceExhausted, sizeMessage, errors.New("private cause"))), sizeMessage},
		{"empty application message", apperror.Wrap(apperror.CodeInternal, "", errors.New("private cause")), "Desktop operation failed"},
		{"empty unknown error", errors.New(""), "Desktop operation failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Wails Callback constructs new Error(message.error). Its JSON error
			// value must be a string, otherwise the renderer sees [object Object].
			wire, err := json.Marshal(struct {
				Error any `json:"error"`
			}{Error: formatDesktopBindingError(test.err)})
			if err != nil {
				t.Fatal(err)
			}
			var callback struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(wire, &callback); err != nil {
				t.Fatalf("Wails callback did not receive a string message: %s (%v)", wire, err)
			}
			if callback.Error != test.want {
				t.Fatalf("callback message = %q, want %q", callback.Error, test.want)
			}
		})
	}
}
