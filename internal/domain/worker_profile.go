package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const MaxWorkerTools = 128

// WorkerProfile opts an admitted child into execution tools. A nil profile keeps
// legacy behavior. Nil Tools inherits the parent's current advertised tools;
// an explicit empty slice permits no tools. Dispatch still checks live authority.
type WorkerProfile struct {
	Tools []string `json:"tools"`
}

func NormalizeWorkerProfile(profile *WorkerProfile) (*WorkerProfile, error) {
	if profile == nil {
		return nil, nil
	}
	if profile.Tools == nil {
		return &WorkerProfile{}, nil
	}
	if len(profile.Tools) > MaxWorkerTools {
		return nil, fmt.Errorf("worker tools exceed %d entries", MaxWorkerTools)
	}
	tools := make([]string, 0, len(profile.Tools))
	seen := make(map[string]bool, len(profile.Tools))
	for _, raw := range profile.Tools {
		tool := strings.TrimSpace(raw)
		if tool == "" || len(tool) > 128 {
			return nil, errors.New("worker tool name must contain between 1 and 128 characters")
		}
		for _, char := range tool {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
				return nil, errors.New("worker tool name contains an unsupported character")
			}
		}
		if !seen[tool] {
			seen[tool] = true
			tools = append(tools, tool)
		}
	}
	sort.Strings(tools)
	return &WorkerProfile{Tools: tools}, nil
}

func (p *WorkerProfile) Validate() error {
	normalized, err := NormalizeWorkerProfile(p)
	if err != nil {
		return err
	}
	if !EqualWorkerProfiles(p, normalized) {
		return errors.New("worker profile must be normalized, unique and sorted")
	}
	return nil
}

func EqualWorkerProfiles(left, right *WorkerProfile) bool {
	if left == nil || right == nil {
		return left == right
	}
	return (left.Tools == nil) == (right.Tools == nil) && slices.Equal(left.Tools, right.Tools)
}

// AllowsTool applies delegated narrowing; the host separately intersects the
// result with the parent's current advertised catalogue.
func (p *WorkerProfile) AllowsTool(name string) bool {
	return p != nil && (p.Tools == nil || slices.Contains(p.Tools, name))
}

func WorkerProfileJSON(profile *WorkerProfile) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(profile)
	return string(raw), err
}
