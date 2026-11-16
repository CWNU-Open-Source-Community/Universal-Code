//go:build !windows

package desktopbrowser

import "context"

type Dispatcher struct{}

func NewDispatcher() (*Dispatcher, error)                      { return nil, ErrUnsupported }
func (*Dispatcher) ThreadID() uint32                           { return 0 }
func (*Dispatcher) Invoke(context.Context, func() error) error { return ErrUnsupported }
func (*Dispatcher) Post(func()) error                          { return ErrUnsupported }
func (*Dispatcher) Close() error                               { return nil }
func DPI(uintptr) uint32                                       { return 96 }

type Host struct{}

func NewHost(context.Context, HostOptions) (*Host, error)         { return nil, ErrUnsupported }
func (*Host) CreateTab(context.Context, TabOptions) (Page, error) { return nil, ErrUnsupported }
func (*Host) Tab(string) (Page, bool)                             { return nil, false }
func (*Host) Close(context.Context) error                         { return nil }
func (*Host) AcquireControl(context.Context, string) (*ControlLease, error) {
	return nil, ErrUnsupported
}

type ControlLease struct{}

func (*ControlLease) Check(context.Context) error                  { return ErrUnsupported }
func (*ControlLease) Done() <-chan struct{}                        { ch := make(chan struct{}); close(ch); return ch }
func (*ControlLease) Cancel()                                      {}
func (*ControlLease) Release(context.Context) error                { return nil }
func (*ControlLease) Dispatch(context.Context, func() error) error { return ErrUnsupported }
func (*ControlLease) InstallNetworkPolicy(context.Context, NetworkPolicy) error {
	return ErrUnsupported
}
