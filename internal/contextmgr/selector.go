package contextmgr

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"cyberagent-workbench/internal/redact"
)

const (
	MaxContextSections     = 512
	MaxContextSectionBytes = 128 * 1024
)

var ErrRequiredContextBudget = errors.New("required context exceeds the token budget")

type Section struct {
	Kind     string
	SourceID string
	// Digest of a verified stored original, before request-local redaction.
	SourceContentSHA256 string `json:",omitempty"`
	Content             string
	Priority            int
	Required            bool `json:",omitempty"`
	Excluded            bool `json:",omitempty"`
	handoff             *modelHandoff
	operatorExcerpt     *Message
}

type Source struct {
	Kind           string
	SourceID       string
	Tokens         int
	OmissionReason string `json:",omitempty"`
}

type Selection struct {
	Sections        []Section
	IncludedSources []Source
	OmittedSources  []Source
	EstimatedTokens int
	TokenBudget     int
}

func SelectSections(sections []Section, maxTokens int) (Selection, error) {
	if maxTokens <= 0 {
		return Selection{}, errors.New("context token budget must be positive")
	}
	if len(sections) > MaxContextSections {
		return Selection{}, fmt.Errorf("context section list exceeds %d items", MaxContextSections)
	}
	type candidate struct {
		section Section
		tokens  int
		order   int
	}
	candidates := make([]candidate, 0, len(sections))
	seen := make(map[string]struct{}, len(sections))
	requiredTokens := 0
	for index, section := range sections {
		section.Kind = strings.TrimSpace(section.Kind)
		section.SourceID = strings.TrimSpace(section.SourceID)
		if section.handoff == nil {
			section.Content = strings.TrimSpace(redact.String(section.Content))
		} else {
			// The handoff masks each source separately and preserves the bound
			// model text. Applying a text regex across entries can join sources.
			section = section.WithReplayedOperators(nil)
		}
		if section.Kind == "" || section.SourceID == "" {
			return Selection{}, errors.New("context section kind and source id are required")
		}
		if section.Priority < 0 || section.Priority > 1000 {
			return Selection{}, fmt.Errorf("context section priority %d is outside 0..1000", section.Priority)
		}
		// Excluded sources are audited but never delivered, so their serialized
		// size must not block delivery of the remaining validated sources.
		if !section.Excluded && len([]byte(section.Content)) > MaxContextSectionBytes {
			if section.Required {
				return Selection{}, fmt.Errorf("%w: required section exceeds %d bytes", ErrRequiredContextBudget, MaxContextSectionBytes)
			}
			return Selection{}, fmt.Errorf("context section %s/%s exceeds %d bytes", section.Kind, section.SourceID, MaxContextSectionBytes)
		}
		if section.Content == "" {
			if section.Required {
				return Selection{}, errors.New("required context section is empty")
			}
			continue
		}
		if section.Required && section.Excluded {
			return Selection{}, errors.New("required context section cannot be excluded")
		}
		key := section.Kind + "\x00" + section.SourceID
		if _, ok := seen[key]; ok {
			return Selection{}, fmt.Errorf("duplicate context source %s/%s", section.Kind, section.SourceID)
		}
		seen[key] = struct{}{}
		tokens := EstimateTokens(section.Content)
		if section.Required {
			if tokens > maxTokens-requiredTokens {
				return Selection{}, ErrRequiredContextBudget
			}
			requiredTokens += tokens
		}
		candidates = append(candidates, candidate{section: section, tokens: tokens, order: index})
	}
	sort.SliceStable(candidates, func(left int, right int) bool {
		if candidates[left].section.Priority == candidates[right].section.Priority {
			return candidates[left].order < candidates[right].order
		}
		return candidates[left].section.Priority > candidates[right].section.Priority
	})
	selection := Selection{
		Sections: make([]Section, 0, len(candidates)), IncludedSources: make([]Source, 0, len(candidates)),
		OmittedSources: make([]Source, 0), TokenBudget: maxTokens,
	}
	optionalTokens := maxTokens - requiredTokens
	for _, candidate := range candidates {
		source := Source{Kind: candidate.section.Kind, SourceID: candidate.section.SourceID, Tokens: candidate.tokens}
		if candidate.section.Excluded {
			source.OmissionReason = "operator_excluded"
			selection.OmittedSources = append(selection.OmittedSources, source)
			continue
		}
		if !candidate.section.Required && candidate.tokens > optionalTokens {
			source.OmissionReason = "budget"
			selection.OmittedSources = append(selection.OmittedSources, source)
			continue
		}
		if !candidate.section.Required {
			optionalTokens -= candidate.tokens
		}
		selection.Sections = append(selection.Sections, candidate.section)
		selection.IncludedSources = append(selection.IncludedSources, source)
		selection.EstimatedTokens += candidate.tokens
	}
	return selection, nil
}
