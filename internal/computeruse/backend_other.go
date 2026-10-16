//go:build !windows

package computeruse

import (
	"context"
	"errors"
)

type unavailable struct{}

func newBackend() backend { return unavailable{} }

var errUnavailable = errors.New("computer use is available only on an interactive Windows desktop")

func (unavailable) enter(context.Context) (func(), error)                 { return nil, errUnavailable }
func (unavailable) generation(bool) uint64                                { return 0 }
func (unavailable) list(context.Context) ([]nativeWindow, error)          { return nil, errUnavailable }
func (unavailable) inspect(uintptr) (nativeWindow, error)                 { return nativeWindow{}, errUnavailable }
func (unavailable) capture(context.Context, nativeWindow) ([]byte, error) { return nil, errUnavailable }
func (unavailable) activate(context.Context, nativeWindow) error          { return errUnavailable }
func (unavailable) input(context.Context, nativeWindow, string, Arguments) error {
	return errUnavailable
}
