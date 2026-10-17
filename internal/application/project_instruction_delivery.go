package application

import (
	"context"
	"encoding/json"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
)

type projectInstructionToolContextStore interface {
	CheckRunProjectInstructionToolContext(context.Context, domain.SupervisorCheckpoint,
		domain.SupervisorToolRound, string) error
}

func (s *AgentRunner) requireCurrentPinnedProjectInstructions(ctx context.Context, run domain.Run) error {
	current, err := s.store.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	var pinned, latest projectconfig.InstructionSnapshot
	if len(run.Config.ProjectInstructions) > 0 {
		if err := json.Unmarshal(run.Config.ProjectInstructions, &pinned); err != nil {
			return err
		}
	}
	if len(current.Config.ProjectInstructions) > 0 {
		if err := json.Unmarshal(current.Config.ProjectInstructions, &latest); err != nil {
			return err
		}
	}
	if (pinned.Delivery != nil || latest.Delivery != nil) &&
		run.Config.ProjectInstructionsFingerprint != current.Config.ProjectInstructionsFingerprint {
		return apperror.New(apperror.CodeFailedPrecondition,
			"pinned project instruction contract changed before dispatch; prior context is preserved")
	}
	return nil
}

func (s *AgentRunner) requirePinnedProjectInstructionToolOrigins(ctx context.Context,
	turn domain.SupervisorTurn, rounds []domain.SupervisorToolRound,
) error {
	if err := s.requireCurrentPinnedProjectInstructions(ctx, turn.Run); err != nil {
		return err
	}
	var snapshot projectconfig.InstructionSnapshot
	if len(turn.Run.Config.ProjectInstructions) == 0 {
		return nil
	}
	if err := json.Unmarshal(turn.Run.Config.ProjectInstructions, &snapshot); err != nil {
		return err
	}
	if snapshot.Delivery == nil || len(snapshot.Sources) == 0 {
		return nil
	}
	for _, round := range rounds {
		if round.ProjectHook() {
			continue
		} // Trust is rechecked by Go at hook dispatch.
		pending := false
		for _, call := range round.Calls {
			pending = pending || call.Status == domain.SupervisorToolPending
		}
		if !pending {
			continue
		}
		store, ok := s.store.(projectInstructionToolContextStore)
		if !ok {
			return apperror.New(apperror.CodeFailedPrecondition,
				"resumed tools require pinned project instruction delivery provenance")
		}
		if err := store.CheckRunProjectInstructionToolContext(ctx, turn.Checkpoint, round,
			snapshot.Fingerprint); err != nil {
			return err
		}
	}
	return nil
}

type projectInstructionDeliveryEnvelope struct {
	projectInstructionGuidanceEnvelope
	Delivery *projectconfig.InstructionSourceDelivery `json:"delivery,omitempty"`
}

func pinnedProjectInstructionContextSections(config domain.RunConfig) ([]contextmgr.Section, error) {
	if len(config.ProjectInstructions) == 0 {
		return nil, nil
	}
	var snapshot projectconfig.InstructionSnapshot
	if err := json.Unmarshal(config.ProjectInstructions, &snapshot); err != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"pinned project instruction snapshot cannot be decoded")
	}
	if err := snapshot.Validate(); err != nil || snapshot.Fingerprint != config.ProjectInstructionsFingerprint {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"pinned project instruction snapshot failed its fingerprint binding")
	}
	sections := make([]contextmgr.Section, 0, len(snapshot.Sources))
	for index, source := range snapshot.Sources {
		envelope := projectInstructionDeliveryEnvelope{projectInstructionGuidanceEnvelope: projectInstructionGuidanceEnvelope{
			Version: "project_instruction_guidance.v1",
			Source: projectInstructionGuidanceSource{Path: source.Path, Scope: source.Scope, Kind: source.Kind,
				ContentSHA256: source.ContentSHA256, Snapshot: snapshot.Fingerprint,
				Precedence: source.Precedence, WhyEffective: source.WhyEffective, Trust: source.Trust},
			Authority: source.Authority, Content: source.Content}}
		section := contextmgr.Section{Kind: "project_instruction", SourceID: source.Path,
			Priority: min(799, 760+source.Depth)}
		if snapshot.Delivery != nil {
			item := snapshot.Delivery.Sources[index]
			envelope.Delivery = &item
			section.SourceID = snapshot.DeliverySourceID(index)
			section.Required = item.Requirement == projectconfig.InstructionMandatory
			section.Excluded = item.Requirement == projectconfig.InstructionExcluded
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		section.Content = strings.TrimSpace(redact.String(string(encoded)))
		sections = append(sections, section)
	}
	return sections, nil
}

func requirePinnedProjectInstructionSelection(config domain.RunConfig, selection contextmgr.Selection) error {
	sections, err := pinnedProjectInstructionContextSections(config)
	if err != nil {
		return err
	}
	for _, required := range sections {
		if !required.Required {
			continue
		}
		found := false
		for _, section := range selection.Sections {
			if section.Kind == required.Kind && section.SourceID == required.SourceID &&
				section.Content == required.Content && section.Required {
				found = true
				break
			}
		}
		if !found || !containsContextSource(selection.IncludedSources, required.Kind, required.SourceID) {
			return projectInstructionDeliveryFailure()
		}
	}
	return nil
}

func requirePinnedProjectInstructionRequest(config domain.RunConfig, request llm.ChatRequest) error {
	sections, err := pinnedProjectInstructionContextSections(config)
	if err != nil {
		return err
	}
	for _, required := range sections {
		if !required.Required {
			continue
		}
		found := false
		for _, message := range request.Messages {
			if message.Role == "user" && message.Content == required.Content {
				found = true
				break
			}
		}
		if !found {
			return projectInstructionDeliveryFailure()
		}
	}
	return nil
}

func projectInstructionDeliveryFailure() error {
	return supervisorContextWindowFailure(apperror.New(apperror.CodeResourceExhausted,
		"mandatory pinned project instructions could not be delivered; history is preserved"))
}
