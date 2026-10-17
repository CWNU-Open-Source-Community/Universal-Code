package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxAgentSessionTitleRunes = 256
	MaxAgentTokenReservation  = int64(1<<62 - 1)
)

type SpecialistAdmission struct {
	AllowedReadPaths []string `json:"allowed_read_paths,omitempty"`
	ToolCallLimit    int64    `json:"tool_call_limit,omitempty"`
	CostLimitMicros  int64    `json:"cost_limit_micros,omitempty"`
	AgentID          string
	SessionID        string
	RunID            string
	ParentAgentID    string
	Title            string
	Skills           []string
	TurnLimit        int64
	TokenLimit       int64
	MaxChildren      int
	CreatedAt        time.Time
}

func (a SpecialistAdmission) Validate() error {
	if err := ValidateSpecialistReadLimits(a.AllowedReadPaths, a.ToolCallLimit, a.CostLimitMicros); err != nil {
		return err
	}
	for _, value := range []string{a.AgentID, a.SessionID, a.RunID, a.ParentAgentID} {
		if !validAgentIdentity(value, false) {
			return errors.New("specialist admission identities are required and must be normalized")
		}
	}
	if !utf8.ValidString(a.Title) || strings.TrimSpace(a.Title) != a.Title || a.Title == "" ||
		utf8.RuneCountInString(a.Title) > MaxAgentSessionTitleRunes {
		return fmt.Errorf("specialist session title must contain between 1 and %d characters",
			MaxAgentSessionTitleRunes)
	}
	normalizedSkills, err := NormalizeAgentSkills(a.Skills)
	if err != nil {
		return err
	}
	if len(normalizedSkills) == 0 || !slices.Equal(normalizedSkills, a.Skills) {
		return errors.New("specialist skills must be a nonempty normalized set")
	}
	if a.TurnLimit <= 0 || a.TokenLimit <= 0 || a.TokenLimit > MaxAgentTokenReservation {
		return errors.New("specialist turn and token reservations must be positive and bounded")
	}
	if a.MaxChildren <= 0 || a.MaxChildren > MaxAgentChildren {
		return fmt.Errorf("specialist capacity must be between 1 and %d", MaxAgentChildren)
	}
	if a.CreatedAt.IsZero() {
		return errors.New("specialist admission creation time is required")
	}
	return nil
}
