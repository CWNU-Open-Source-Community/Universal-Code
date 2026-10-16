package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/runmutation"
)

const (
	PlanDeliveryProtocolVersion   = "plan_delivery.v1"
	MaxPlanDeliveryJSONBytes      = 80 * 1024
	MinPlanDeliveryDirections     = 1
	PlanDeliveryDirectionCount    = 3
	MaxPlanDeliveryModules        = 8
	MaxPlanDeliveryTitleRunes     = 240
	MaxPlanDeliverySummaryRunes   = 1200
	MaxPlanDeliveryObjectiveRunes = 2400
	MaxPlanDeliveryListItems      = 8
	MaxPlanDeliveryListItemRunes  = 512
)

type PlanDeliveryModule struct {
	Ordinal            int      `json:"-"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Dependencies       []int    `json:"dependencies"`
}

type PlanDeliveryDirection struct {
	Ordinal   int                  `json:"-"`
	Title     string               `json:"title"`
	Summary   string               `json:"summary"`
	Tradeoffs []string             `json:"tradeoffs"`
	Modules   []PlanDeliveryModule `json:"modules"`
}

type PlanDeliverySpec struct {
	Version    string                  `json:"version"`
	Directions []PlanDeliveryDirection `json:"directions"`
}

type PlanDeliveryProposalStatus string

const PlanDeliveryProposalProposed PlanDeliveryProposalStatus = "proposed"

type PlanDeliveryProposal struct {
	ID           string
	RunID        string
	RootAgentID  string
	SessionID    string
	WorkspaceID  string
	ModeRevision int64
	Status       PlanDeliveryProposalStatus
	Spec         PlanDeliverySpec
	Fingerprint  string
	RequestedBy  string
	Version      int64
	CreatedAt    time.Time
}

type PlanDeliveryProposalOperation struct {
	KeyDigest          string
	RequestFingerprint string
	InvocationID       string
	ProposalID         string
	RunID              string
	SessionID          string
	WorkspaceID        string
	RootAgentID        string
	LeaseID            string
	LeaseGeneration    int64
	RequestedBy        string
	CreatedAt          time.Time
}

type PlanDeliverySelectionItem struct {
	Ordinal       int
	ModuleOrdinal int
	WorkItemID    string
}

// PlanDeliveryManualAcceptance is chosen by the operator, never by the model's
// proposal. Empty legacy values retain the original required checkpoint gate.
type PlanDeliveryManualAcceptance string

const (
	PlanDeliveryManualAcceptanceRequired PlanDeliveryManualAcceptance = "required"
	PlanDeliveryManualAcceptanceOnDemand PlanDeliveryManualAcceptance = "on_demand"
)

func NormalizePlanDeliveryManualAcceptance(value PlanDeliveryManualAcceptance) (PlanDeliveryManualAcceptance, error) {
	switch value {
	case "", PlanDeliveryManualAcceptanceRequired:
		return PlanDeliveryManualAcceptanceRequired, nil
	case PlanDeliveryManualAcceptanceOnDemand:
		return value, nil
	default:
		return "", errors.New("manual acceptance must be required or on_demand")
	}
}

type PlanDeliverySelection struct {
	ID               string
	ProposalID       string
	RunID            string
	RootAgentID      string
	DirectionOrdinal int
	ManualAcceptance PlanDeliveryManualAcceptance
	NoteID           string
	Items            []PlanDeliverySelectionItem
	RequestedBy      string
	Version          int64
	CreatedAt        time.Time
}

func (s PlanDeliverySelection) EffectiveManualAcceptance() PlanDeliveryManualAcceptance {
	if s.ManualAcceptance == "" {
		return PlanDeliveryManualAcceptanceRequired
	}
	return s.ManualAcceptance
}

type PlanDeliverySelectionOperation struct {
	KeyDigest          string
	RequestFingerprint string
	SelectionID        string
	ProposalID         string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

func DecodePlanDeliverySpec(raw []byte) (PlanDeliverySpec, error) {
	if len(raw) == 0 || len(raw) > MaxPlanDeliveryJSONBytes || !utf8.Valid(raw) {
		return PlanDeliverySpec{}, fmt.Errorf(
			"Plan/Delivery payload must be valid UTF-8 JSON within %d bytes",
			MaxPlanDeliveryJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var spec PlanDeliverySpec
	if err := decoder.Decode(&spec); err != nil {
		return PlanDeliverySpec{}, errors.New(
			"Plan/Delivery payload does not match plan_delivery.v1")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PlanDeliverySpec{}, errors.New("Plan/Delivery payload contains trailing data")
	}
	return NormalizePlanDeliverySpec(spec)
}

func NormalizePlanDeliverySpec(spec PlanDeliverySpec) (PlanDeliverySpec, error) {
	originalVersion := spec.Version
	spec.Version = strings.TrimSpace(spec.Version)
	if originalVersion != spec.Version || spec.Version != PlanDeliveryProtocolVersion {
		return PlanDeliverySpec{}, fmt.Errorf("unsupported Plan/Delivery version %q", spec.Version)
	}
	if len(spec.Directions) < MinPlanDeliveryDirections || len(spec.Directions) > PlanDeliveryDirectionCount {
		return PlanDeliverySpec{}, fmt.Errorf(
			"Plan/Delivery requires between %d and %d directions", MinPlanDeliveryDirections, PlanDeliveryDirectionCount)
	}
	normalized := make([]PlanDeliveryDirection, len(spec.Directions))
	seenDirections := make(map[string]struct{}, len(spec.Directions))
	for directionIndex, direction := range spec.Directions {
		direction.Ordinal = directionIndex + 1
		var err error
		direction.Title, err = normalizePlanDeliveryText(direction.Title,
			"direction title", MaxPlanDeliveryTitleRunes)
		if err != nil {
			return PlanDeliverySpec{}, fmt.Errorf("direction %d: %w", direction.Ordinal, err)
		}
		direction.Summary, err = normalizePlanDeliveryText(direction.Summary,
			"direction summary", MaxPlanDeliverySummaryRunes)
		if err != nil {
			return PlanDeliverySpec{}, fmt.Errorf("direction %d: %w", direction.Ordinal, err)
		}
		direction.Tradeoffs, err = normalizePlanDeliveryTextList(direction.Tradeoffs,
			"tradeoff", 1, MaxPlanDeliveryListItems)
		if err != nil {
			return PlanDeliverySpec{}, fmt.Errorf("direction %d: %w", direction.Ordinal, err)
		}
		if len(direction.Modules) == 0 || len(direction.Modules) > MaxPlanDeliveryModules {
			return PlanDeliverySpec{}, fmt.Errorf(
				"direction %d requires between 1 and %d modules",
				direction.Ordinal, MaxPlanDeliveryModules)
		}
		identity := strings.ToLower(direction.Title)
		if _, duplicate := seenDirections[identity]; duplicate {
			return PlanDeliverySpec{}, errors.New("Plan/Delivery directions must have distinct titles")
		}
		seenDirections[identity] = struct{}{}
		direction.Modules = slices.Clone(direction.Modules)
		seenModules := make(map[string]struct{}, len(direction.Modules))
		for moduleIndex, module := range direction.Modules {
			module.Ordinal = moduleIndex + 1
			module.Title, err = normalizePlanDeliveryText(module.Title,
				"module title", MaxPlanDeliveryTitleRunes)
			if err != nil {
				return PlanDeliverySpec{}, fmt.Errorf(
					"direction %d module %d: %w", direction.Ordinal, module.Ordinal, err)
			}
			module.Objective, err = normalizePlanDeliveryText(module.Objective,
				"module objective", MaxPlanDeliveryObjectiveRunes)
			if err != nil {
				return PlanDeliverySpec{}, fmt.Errorf(
					"direction %d module %d: %w", direction.Ordinal, module.Ordinal, err)
			}
			module.AcceptanceCriteria, err = normalizePlanDeliveryTextList(
				module.AcceptanceCriteria, "acceptance criterion", 1, MaxPlanDeliveryListItems)
			if err != nil {
				return PlanDeliverySpec{}, fmt.Errorf(
					"direction %d module %d: %w", direction.Ordinal, module.Ordinal, err)
			}
			module.Dependencies, err = normalizePlanDeliveryDependencies(
				module.Dependencies, module.Ordinal)
			if err != nil {
				return PlanDeliverySpec{}, fmt.Errorf(
					"direction %d module %d: %w", direction.Ordinal, module.Ordinal, err)
			}
			moduleIdentity := strings.ToLower(module.Title)
			if _, duplicate := seenModules[moduleIdentity]; duplicate {
				return PlanDeliverySpec{}, fmt.Errorf(
					"direction %d modules must have distinct titles", direction.Ordinal)
			}
			seenModules[moduleIdentity] = struct{}{}
			direction.Modules[moduleIndex] = module
		}
		if err := validatePlanDeliveryDirectionProjection(direction); err != nil {
			return PlanDeliverySpec{}, fmt.Errorf("direction %d: %w", direction.Ordinal, err)
		}
		normalized[directionIndex] = direction
	}
	spec.Directions = normalized
	encoded, err := json.Marshal(spec)
	if err != nil || len(encoded) > MaxPlanDeliveryJSONBytes {
		return PlanDeliverySpec{}, fmt.Errorf(
			"normalized Plan/Delivery payload exceeds %d bytes",
			MaxPlanDeliveryJSONBytes)
	}
	return spec, nil
}

func (s PlanDeliverySpec) Validate() error {
	normalized, err := NormalizePlanDeliverySpec(s)
	if err != nil {
		return err
	}
	if !slices.EqualFunc(normalized.Directions, s.Directions,
		func(left, right PlanDeliveryDirection) bool {
			return left.Ordinal == right.Ordinal && left.Title == right.Title &&
				left.Summary == right.Summary && slices.Equal(left.Tradeoffs, right.Tradeoffs) &&
				slices.EqualFunc(left.Modules, right.Modules,
					func(leftModule, rightModule PlanDeliveryModule) bool {
						return leftModule.Ordinal == rightModule.Ordinal &&
							leftModule.Title == rightModule.Title &&
							leftModule.Objective == rightModule.Objective &&
							slices.Equal(leftModule.AcceptanceCriteria,
								rightModule.AcceptanceCriteria) &&
							slices.Equal(leftModule.Dependencies, rightModule.Dependencies)
					})
		}) {
		return errors.New("Plan/Delivery specification must be normalized")
	}
	return nil
}

func (p PlanDeliveryProposal) Validate() error {
	for _, value := range []string{p.ID, p.RunID, p.RootAgentID, p.SessionID, p.RequestedBy} {
		if !validAgentIdentity(value, false) {
			return errors.New("Plan/Delivery proposal identities are required and normalized")
		}
	}
	if !validAgentIdentity(p.WorkspaceID, true) || p.ModeRevision <= 0 {
		return errors.New("Plan/Delivery proposal scope and mode revision are invalid")
	}
	if p.Status != PlanDeliveryProposalProposed || p.Version != 1 || p.CreatedAt.IsZero() {
		return errors.New("Plan/Delivery proposal status, version, and timestamp are invalid")
	}
	if err := p.Spec.Validate(); err != nil {
		return err
	}
	if !validLowerHexDigest(p.Fingerprint) || p.Fingerprint != PlanDeliveryProposalFingerprint(p) {
		return errors.New("Plan/Delivery proposal fingerprint is invalid")
	}
	return nil
}

func (o PlanDeliveryProposalOperation) Validate() error {
	if err := o.validatePersistableFields(); err != nil {
		return err
	}
	if !validAgentIdentity(o.LeaseID, false) || o.LeaseGeneration <= 0 {
		return errors.New("Plan/Delivery proposal operation requires an active lease")
	}
	return nil
}

func (o PlanDeliveryProposalOperation) ValidatePersisted() error {
	if o.LeaseID != "" || o.LeaseGeneration != 0 {
		return errors.New("persisted Plan/Delivery proposal operation contains lease identity")
	}
	return o.validatePersistableFields()
}

func (o PlanDeliveryProposalOperation) validatePersistableFields() error {
	for _, value := range []string{o.InvocationID, o.ProposalID, o.RunID, o.SessionID,
		o.RootAgentID, o.RequestedBy} {
		if !validAgentIdentity(value, false) {
			return errors.New("Plan/Delivery proposal operation identities are invalid")
		}
	}
	if !validAgentIdentity(o.WorkspaceID, true) || !validLowerHexDigest(o.KeyDigest) ||
		!validLowerHexDigest(o.RequestFingerprint) || o.CreatedAt.IsZero() {
		return errors.New("Plan/Delivery proposal operation scope or digest is invalid")
	}
	return nil
}

func (s PlanDeliverySelection) Validate() error {
	if _, err := NormalizePlanDeliveryManualAcceptance(s.ManualAcceptance); err != nil {
		return err
	}
	for _, value := range []string{s.ID, s.ProposalID, s.RunID, s.RootAgentID,
		s.NoteID, s.RequestedBy} {
		if !validAgentIdentity(value, false) {
			return errors.New("Plan/Delivery selection identities are invalid")
		}
	}
	if s.DirectionOrdinal < 1 || s.DirectionOrdinal > PlanDeliveryDirectionCount ||
		len(s.Items) == 0 || len(s.Items) > MaxPlanDeliveryModules || s.Version != 1 ||
		s.CreatedAt.IsZero() {
		return errors.New("Plan/Delivery selection shape, version, or timestamp is invalid")
	}
	seen := make(map[string]struct{}, len(s.Items))
	for index, item := range s.Items {
		if item.Ordinal != index+1 || item.ModuleOrdinal != item.Ordinal ||
			!validAgentIdentity(item.WorkItemID, false) {
			return fmt.Errorf("Plan/Delivery selection item %d is invalid", index+1)
		}
		if _, duplicate := seen[item.WorkItemID]; duplicate {
			return errors.New("Plan/Delivery selection WorkItem identities must be unique")
		}
		seen[item.WorkItemID] = struct{}{}
	}
	return nil
}

func (o PlanDeliverySelectionOperation) Validate() error {
	for _, value := range []string{o.SelectionID, o.ProposalID, o.RunID, o.RequestedBy} {
		if !validAgentIdentity(value, false) {
			return errors.New("Plan/Delivery selection operation identities are invalid")
		}
	}
	if !validLowerHexDigest(o.KeyDigest) || !validLowerHexDigest(o.RequestFingerprint) ||
		o.CreatedAt.IsZero() {
		return errors.New("Plan/Delivery selection operation digest or timestamp is invalid")
	}
	return nil
}

func PlanDeliveryProposalFingerprint(proposal PlanDeliveryProposal) string {
	encoded, err := json.Marshal(proposal.Spec)
	if err != nil {
		return ""
	}
	return runmutation.Fingerprint(PlanDeliveryProtocolVersion, proposal.RunID,
		proposal.RootAgentID, proposal.SessionID, proposal.WorkspaceID,
		fmt.Sprint(proposal.ModeRevision), proposal.RequestedBy, string(encoded))
}

func PlanDeliveryProposalRequestFingerprint(proposal PlanDeliveryProposal) string {
	return runmutation.Fingerprint("plan_delivery_proposal_request.v1",
		proposal.RunID, proposal.RootAgentID, proposal.SessionID,
		proposal.WorkspaceID, fmt.Sprint(proposal.ModeRevision),
		proposal.RequestedBy, proposal.Fingerprint)
}

func PlanDeliverySelectionRequestFingerprint(proposalID, runID string,
	directionOrdinal int, requestedBy string,
) string {
	return runmutation.Fingerprint("plan_delivery_selection_request.v1", proposalID,
		runID, fmt.Sprint(directionOrdinal), requestedBy)
}

func PlanDeliverySelectionRequestFingerprintForAcceptance(proposalID, runID string,
	directionOrdinal int, requestedBy string, acceptance PlanDeliveryManualAcceptance,
) string {
	mode, err := NormalizePlanDeliveryManualAcceptance(acceptance)
	if err != nil {
		return ""
	}
	if mode == PlanDeliveryManualAcceptanceRequired {
		return PlanDeliverySelectionRequestFingerprint(proposalID, runID, directionOrdinal, requestedBy)
	}
	return runmutation.Fingerprint("plan_delivery_selection_request.v1", proposalID,
		runID, fmt.Sprint(directionOrdinal), requestedBy, string(mode))
}

func PlanDeliveryHandoffTitle(direction PlanDeliveryDirection) string {
	return fmt.Sprintf("Accepted plan direction %d", direction.Ordinal)
}

func PlanDeliveryHandoffContent(proposal PlanDeliveryProposal,
	direction PlanDeliveryDirection,
) string {
	var builder strings.Builder
	builder.WriteString("Plan/Delivery decision\n\n")
	builder.WriteString("Proposal: ")
	builder.WriteString(proposal.ID)
	builder.WriteString("\nDirection: ")
	builder.WriteString(strconv.Itoa(direction.Ordinal))
	builder.WriteString(" - ")
	builder.WriteString(direction.Title)
	builder.WriteString("\n\nSummary\n")
	builder.WriteString(direction.Summary)
	builder.WriteString("\n\nTradeoffs\n")
	for _, tradeoff := range direction.Tradeoffs {
		builder.WriteString("- ")
		builder.WriteString(tradeoff)
		builder.WriteByte('\n')
	}
	builder.WriteString("\nDelivery slices\n")
	for _, module := range direction.Modules {
		builder.WriteString(strconv.Itoa(module.Ordinal))
		builder.WriteString(". ")
		builder.WriteString(module.Title)
		builder.WriteString("\n   Objective: ")
		builder.WriteString(module.Objective)
		builder.WriteString("\n   Acceptance:\n")
		for _, criterion := range module.AcceptanceCriteria {
			builder.WriteString("   - ")
			builder.WriteString(criterion)
			builder.WriteByte('\n')
		}
		if len(module.Dependencies) > 0 {
			builder.WriteString("   Depends on slices: ")
			for index, dependency := range module.Dependencies {
				if index > 0 {
					builder.WriteString(", ")
				}
				builder.WriteString(strconv.Itoa(dependency))
			}
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("\nControl boundary\nThe selected direction creates planning records only. It does not change the Run phase or grant file, Shell, process, network, or child-Agent capability.")
	return builder.String()
}

func validatePlanDeliveryDirectionProjection(direction PlanDeliveryDirection) error {
	if utf8.RuneCountInString(PlanDeliveryHandoffTitle(direction)) > MaxNoteTitleRunes {
		return errors.New("Plan/Delivery handoff Note title exceeds its durable bound")
	}
	// Reserve the maximum UTF-8 byte width of a valid proposal identity so any
	// accepted direction is guaranteed to remain selectable after persistence.
	maxIdentityBytes := strings.Repeat("x", MaxAgentIdentityRunes*utf8.UTFMax)
	content := PlanDeliveryHandoffContent(PlanDeliveryProposal{ID: maxIdentityBytes},
		direction)
	if len([]byte(content)) > MaxNoteContentBytes {
		return fmt.Errorf("Plan/Delivery handoff Note exceeds %d bytes",
			MaxNoteContentBytes)
	}
	return nil
}

func ClonePlanDeliveryProposal(proposal PlanDeliveryProposal) PlanDeliveryProposal {
	proposal.Spec.Directions = slices.Clone(proposal.Spec.Directions)
	for directionIndex := range proposal.Spec.Directions {
		direction := &proposal.Spec.Directions[directionIndex]
		direction.Tradeoffs = slices.Clone(direction.Tradeoffs)
		direction.Modules = slices.Clone(direction.Modules)
		for moduleIndex := range direction.Modules {
			direction.Modules[moduleIndex].AcceptanceCriteria = slices.Clone(
				direction.Modules[moduleIndex].AcceptanceCriteria)
			direction.Modules[moduleIndex].Dependencies = slices.Clone(
				direction.Modules[moduleIndex].Dependencies)
		}
	}
	return proposal
}

func ClonePlanDeliverySelection(selection PlanDeliverySelection) PlanDeliverySelection {
	selection.Items = slices.Clone(selection.Items)
	return selection
}

func normalizePlanDeliveryText(value, label string, maxRunes int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return "", fmt.Errorf("%s must contain between 1 and %d UTF-8 characters", label, maxRunes)
	}
	for _, current := range value {
		if current == 0 || (unicode.IsControl(current) && current != '\n' && current != '\r' && current != '\t') {
			return "", fmt.Errorf("%s contains a forbidden control character", label)
		}
	}
	return value, nil
}

func normalizePlanDeliveryTextList(values []string, label string, minItems, maxItems int) ([]string, error) {
	if len(values) < minItems || len(values) > maxItems {
		return nil, fmt.Errorf("%s list requires between %d and %d items", label, minItems, maxItems)
	}
	out := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		normalized, err := normalizePlanDeliveryText(value, label, MaxPlanDeliveryListItemRunes)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", index+1, err)
		}
		identity := strings.ToLower(normalized)
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("%s list must contain unique items", label)
		}
		seen[identity] = struct{}{}
		out[index] = normalized
	}
	return out, nil
}

func normalizePlanDeliveryDependencies(values []int, moduleOrdinal int) ([]int, error) {
	if len(values) > MaxPlanDeliveryModules-1 {
		return nil, errors.New("module dependency list is too large")
	}
	// Empty model input must retain the array shape required by the proposal
	// ledger, including when JSON decoded an omitted or null dependency list.
	out := append([]int{}, values...)
	slices.Sort(out)
	compact := slices.Compact(out)
	if len(compact) != len(out) {
		return nil, errors.New("module dependencies must be unique")
	}
	out = compact
	for _, dependency := range out {
		if dependency <= 0 || dependency >= moduleOrdinal {
			return nil, errors.New("module dependencies must reference earlier module ordinals")
		}
	}
	return out, nil
}
