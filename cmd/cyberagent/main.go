package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"cyberagent-workbench/internal/app"
	"cyberagent-workbench/internal/computeruse"
	"cyberagent-workbench/internal/sbxmcp"
)

func main() {
	if handled, code := computeruse.Execute(os.Args[1:], os.Stdin, os.Stdout); handled {
		os.Exit(code)
	}
	if handled, code := sbxmcp.Execute(os.Args[1:], os.Stdin, os.Stdout); handled {
		os.Exit(code)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := app.ExecuteContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
