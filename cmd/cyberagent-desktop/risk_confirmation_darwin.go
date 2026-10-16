//go:build darwin && desktop

package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func confirmDesktopRiskDialog(ctx context.Context, options runtime.MessageDialogOptions, affirmative string) (bool, error) {
	answer, err := runtime.MessageDialog(ctx, options)
	return err == nil && answer == affirmative, err
}
