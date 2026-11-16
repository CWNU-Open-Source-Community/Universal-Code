package llm

import (
	"encoding/base64"
	"errors"
	"strings"

	"cyberagent-workbench/internal/imageattachment"
)

const (
	MaxMessageImages     = 4
	MaxImageBytes        = imageattachment.MaxBytes
	MaxRequestImageBytes = MaxMessageImages * MaxImageBytes
	MaxImageDimension    = imageattachment.MaxDimension
	MaxImagePixels       = imageattachment.MaxPixels
)

// ImagePart holds already-admitted image bytes, never a host path or remote
// URL. Neither the image nor its bytes enter diagnostic JSON serialization.
type ImagePart struct {
	MediaType string `json:"-"`
	Data      []byte `json:"-"`
	SHA256    string `json:"-"`
	Width     int    `json:"-"`
	Height    int    `json:"-"`
}

func ValidateMessageImages(message Message) error {
	if len(message.Images) == 0 {
		return nil
	}
	if strings.TrimSpace(message.Role) != "user" || len(message.Images) > MaxMessageImages {
		return errors.New("images require a user evidence message with at most four images")
	}
	for _, part := range message.Images {
		actual, err := imageattachment.Validate(part.Data, part.MediaType, "")
		if err != nil {
			return err
		}
		if actual.SHA256 != part.SHA256 || actual.Width != part.Width || actual.Height != part.Height {
			return errors.New("image digest or dimensions do not match its bytes")
		}
	}
	return nil
}

// EstimateImageTokens is a local planning estimate, not a billing calculator
// or a universal upper bound. Reserve unscaled 28px visual patches with a 2x
// allowance and an additional 1024 tokens. This covers common patch-based
// protocols without applying one unusually expensive model's tile rate to
// every compatible route. Gateways (notably high-cost tile models) can charge
// more; their capacity errors must remain visible. Never discard images or
// silently enlarge the configured window to make this estimate fit.
func EstimateImageTokens(part ImagePart) int {
	if part.Width < 1 || part.Height < 1 || part.Width > MaxImageDimension || part.Height > MaxImageDimension {
		return 1_000_000
	}
	return 1024 + ((part.Width+27)/28)*((part.Height+27)/28)*2
}

func imageDataURL(part ImagePart) string {
	return "data:" + part.MediaType + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
}

type VisionSupport string

const (
	VisionSupported   VisionSupport = "supported"
	VisionUnsupported VisionSupport = "unsupported"
	VisionUnknown     VisionSupport = "unknown"
)

type VisionCapability struct {
	State  VisionSupport `json:"state"`
	Source string        `json:"source"`
}

// VisionDescriber reports exact model capability provenance. Protocol support
// alone is not evidence of model vision support.
type VisionDescriber interface {
	DescribeVision(model string) VisionCapability
}

func runtimeVision(runtime HTTPProviderRuntime, model string) VisionCapability {
	if describer, ok := runtime.(VisionDescriber); ok {
		value := describer.DescribeVision(model)
		if value.State == VisionSupported || value.State == VisionUnsupported || value.State == VisionUnknown {
			return value
		}
	}
	return VisionCapability{State: VisionUnknown, Source: "unknown"}
}

func providerVision(provider Provider, model string) VisionCapability {
	if describer, ok := provider.(VisionDescriber); ok {
		return describer.DescribeVision(model)
	}
	return VisionCapability{State: VisionUnsupported, Source: "adapter_unsupported"}
}

func validateRequestImages(provider Provider, model string, messages []Message) error {
	totalBytes := 0
	for _, message := range messages {
		if err := ValidateMessageImages(message); err != nil {
			return err
		}
		if len(message.Images) > 0 && providerVision(provider, model).State != VisionSupported {
			return errors.New("model image input capability is not established")
		}
		for _, part := range message.Images {
			totalBytes += len(part.Data)
		}
	}
	// Bound the complete request, including images from earlier messages.
	if totalBytes > MaxRequestImageBytes {
		return errors.New("request image bytes exceed the supported bound")
	}
	return nil
}

func (r *Router) DescribeVision(ref ModelRef) VisionCapability {
	if r == nil {
		return VisionCapability{State: VisionUnknown, Source: "unknown"}
	}
	r.mu.RLock()
	provider, ok := r.providers[ref.Provider]
	r.mu.RUnlock()
	if !ok {
		return VisionCapability{State: VisionUnknown, Source: "unknown"}
	}
	return providerVision(provider, ref.Model)
}

func (p *OllamaProvider) DescribeVision(model string) VisionCapability {
	p.mu.RLock()
	state, ok := p.models[model]
	p.mu.RUnlock()
	if !ok || !state.known {
		return VisionCapability{State: VisionUnknown, Source: "unknown"}
	}
	support := VisionUnsupported
	if state.vision {
		support = VisionSupported
	}
	return VisionCapability{State: support, Source: "provider_metadata"}
}
