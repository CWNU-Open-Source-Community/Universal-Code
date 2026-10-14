package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type WebAccessMode string

const (
	WebAccessDisabled WebAccessMode = "disabled"
	WebAccessRules    WebAccessMode = "rules"
	WebAccessFull     WebAccessMode = "full"
)

func (m WebAccessMode) Valid() bool {
	return m == WebAccessDisabled || m == WebAccessRules || m == WebAccessFull
}

// Omitted modes preserve the historical search and exact-host approval policy.
func (s Scope) EffectiveWebAccessMode() WebAccessMode {
	if s.WebAccessMode != "" {
		return s.WebAccessMode
	}
	for _, target := range s.AllowedTargets {
		if target == "public_https" {
			return WebAccessFull
		}
	}
	return WebAccessRules
}

func (s Scope) EffectiveWebAllowedTargets() []string {
	if s.EffectiveWebAccessMode() != WebAccessRules {
		return nil
	}
	if s.WebAccessMode == "" {
		return append([]string(nil), s.AllowedTargets...)
	}
	return append([]string(nil), s.WebAllowedTargets...)
}

func (s Scope) SameWebAccess(other Scope) bool {
	return s.WebAccessMode == other.WebAccessMode && slices.Equal(s.WebAllowedTargets, other.WebAllowedTargets)
}

func (s Scope) ValidateWebAccess() error {
	if s.WebAccessMode == "" {
		if len(s.WebAllowedTargets) != 0 {
			return errors.New("web targets require an explicit web access mode")
		}
		return nil
	}
	if !s.WebAccessMode.Valid() {
		return fmt.Errorf("unsupported web access mode %q", s.WebAccessMode)
	}
	if s.WebAccessMode != WebAccessRules && len(s.WebAllowedTargets) != 0 {
		return errors.New("web targets apply only to rules mode")
	}
	if len(s.WebAllowedTargets) > 256 {
		return errors.New("web target allowlist exceeds 256 entries")
	}
	for i, target := range s.WebAllowedTargets {
		if target == "" || len(target) > 253 || target != strings.ToLower(strings.TrimSpace(target)) || target == "public_https" || strings.ContainsAny(target, "*:/\\") || (i > 0 && s.WebAllowedTargets[i-1] >= target) {
			return errors.New("web targets must be canonical sorted exact DNS hosts")
		}
	}
	return nil
}
