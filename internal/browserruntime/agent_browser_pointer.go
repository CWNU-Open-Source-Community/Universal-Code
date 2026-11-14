package browserruntime

import (
	"context"
	"math"
)

// AgentBrowserPoint binds computer-use coordinates to the observed document
// and CSS viewport. The user and the Agent still operate the same native page.
type AgentBrowserPoint struct {
	X, Y          float64
	DocumentEpoch uint64
	Viewport      AgentBrowserViewport
}

func (r *AgentBrowserRuntime) currentViewport(ctx context.Context) (AgentBrowserViewport, error) {
	var metrics struct {
		CSSLayoutViewport struct{ ClientWidth, ClientHeight float64 }
	}
	if err := r.call(ctx, "Page.getLayoutMetrics", map[string]any{}, &metrics); err != nil {
		return AgentBrowserViewport{}, err
	}
	w, h := metrics.CSSLayoutViewport.ClientWidth, metrics.CSSLayoutViewport.ClientHeight
	if math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) || w < 1 || h < 1 || w > 16384 || h > 16384 {
		return AgentBrowserViewport{}, ErrBrowserRuntimeBoundary
	}
	return AgentBrowserViewport{Width: int(math.Round(w)), Height: int(math.Round(h))}, nil
}

func (r *AgentBrowserRuntime) pointer(ctx context.Context, p AgentBrowserPoint, click bool) (AgentBrowserInteraction, error) {
	if p.DocumentEpoch == 0 || math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) || p.X < 0 || p.Y < 0 || p.Viewport.Width < 1 || p.Viewport.Height < 1 || p.X >= float64(p.Viewport.Width) || p.Y >= float64(p.Viewport.Height) {
		return AgentBrowserInteraction{}, ErrBrowserRuntimeBoundary
	}
	op, end, err := r.begin(ctx)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	defer end()
	doc, err := r.document(op)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	if doc.epoch != p.DocumentEpoch {
		return AgentBrowserInteraction{}, ErrAgentBrowserStaleReference
	}
	viewport, err := r.currentViewport(op)
	if err != nil {
		return AgentBrowserInteraction{}, err
	}
	if viewport != p.Viewport {
		return AgentBrowserInteraction{}, ErrAgentBrowserStaleReference
	}
	if err = r.check(op, p.DocumentEpoch); err != nil {
		return AgentBrowserInteraction{}, err
	}
	if err = r.input(op, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": p.X, "y": p.Y}); err != nil {
		return AgentBrowserInteraction{}, err
	}
	if !click {
		return r.interaction("move"), nil
	}
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		if err = r.check(op, p.DocumentEpoch); err != nil {
			return AgentBrowserInteraction{}, r.unknown(err)
		}
		viewport, err = r.currentViewport(op)
		if err != nil || viewport != p.Viewport {
			if err == nil {
				err = ErrAgentBrowserStaleReference
			}
			return AgentBrowserInteraction{}, r.unknown(err)
		}
		if err = r.input(op, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": p.X, "y": p.Y, "button": "left", "clickCount": 1}); err != nil {
			return AgentBrowserInteraction{}, err
		}
	}
	return r.interaction("click"), nil
}
func (r *AgentBrowserRuntime) ClickPoint(ctx context.Context, p AgentBrowserPoint) (AgentBrowserInteraction, error) {
	return r.pointer(ctx, p, true)
}
func (r *AgentBrowserRuntime) MovePointer(ctx context.Context, p AgentBrowserPoint) (AgentBrowserInteraction, error) {
	return r.pointer(ctx, p, false)
}
