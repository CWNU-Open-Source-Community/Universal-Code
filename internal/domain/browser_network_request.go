package domain

import "time"

const BrowserNetworkRequestPrefix = "browser-network-"

// BrowserNetworkRequest is the durable source for a single live paused page
// write. Body bytes, headers and URL query values stay in Chromium.
type BrowserNetworkRequest struct {
	ID, RunID, SessionID, WorkspaceID                    string
	ManagerBootID, RuntimeID, Fingerprint, AuthorityJSON string
	Method, DisplayURL, BodySHA256                       string
	BodyBytes                                            int
	ExpiresAt                                            time.Time
	Status                                               string
}
