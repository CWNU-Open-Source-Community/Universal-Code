//go:build !windows && desktop

package main

import "cyberagent-workbench/internal/desktop"

func prepareSharedBrowser(string) (*desktop.SharedBrowserOptions, func(), error) {
	return nil, func() {}, nil
}
