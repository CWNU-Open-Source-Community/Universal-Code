package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	ModelPublicCommentaryVersion = "model_public_commentary.v1"
	MaxPublicCommentaryTextRunes = 4096
	PublicCommentaryBeforeTools  = ModelPublicCommentaryPhase("before_tools")
)

type ModelPublicCommentaryPhase string

// ModelPublicCommentary is display-only model prose. It is never a Session
// message, trusted context, a tool result, or evidence that an action happened.
type ModelPublicCommentary struct {
	Version      string                     `json:"version"`
	RunID        string                     `json:"run_id"`
	AttemptID    string                     `json:"attempt_id"`
	ModelAttempt int                        `json:"model_attempt"`
	ToolRound    int                        `json:"tool_round"`
	Phase        ModelPublicCommentaryPhase `json:"phase"`
	Text         string                     `json:"text"`
}

func (c ModelPublicCommentary) Validate() error {
	if c.Version != ModelPublicCommentaryVersion {
		return errors.New("model public commentary version is invalid")
	}
	if strings.TrimSpace(c.RunID) == "" || strings.TrimSpace(c.AttemptID) == "" {
		return errors.New("model public commentary run and attempt ids are required")
	}
	if c.ModelAttempt <= 0 || c.ToolRound <= 0 || c.ToolRound > MaxSupervisorExecutionRounds {
		return errors.New("model public commentary attempt and tool round are invalid")
	}
	if c.Phase != PublicCommentaryBeforeTools {
		return errors.New("model public commentary phase is invalid")
	}
	text := strings.TrimSpace(c.Text)
	if text == "" || !utf8.ValidString(text) || len([]rune(text)) > MaxPublicCommentaryTextRunes {
		return errors.New("model public commentary text is invalid")
	}
	return nil
}
