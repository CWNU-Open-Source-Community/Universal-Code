//go:build !darwin || !cgo

package browserruntime

import (
	"context"
	"runtime"
)

func AgentBrowserPlatformAvailable() bool { return runtime.GOOS == "windows" }
func LaunchAgentBrowser(ctx context.Context, request AgentBrowserStartRequest) (*AgentBrowserRuntime, error) {
	return launchPEAgentBrowser(ctx, request)
}
