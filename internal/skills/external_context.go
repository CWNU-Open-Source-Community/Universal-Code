package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
)

const (
	ExternalContextProtocolVersion           = "external_skill_context.v1"
	ExternalSpecialistContextProtocolVersion = "external_specialist_skill_context.v1"
	DefaultExternalSpecialistTokenBudget     = 1024
	MaxExternalSpecialistTokenBudget         = 2048
)

// ExternalContextItem carries untrusted workflow guidance in memory. Its
// installation/object provenance is pinned without treating declared tools as
// grants or persisting the body.
type ExternalContextItem struct {
	Plugin                   *PluginSkillBinding `json:",omitempty"`
	Ordinal                  int
	InstallationID           string
	InstallationFingerprint  string
	InstallResultFingerprint string
	Name                     string
	Version                  string
	SourceSHA256             string
	SourceBytes              int
	SourceTokenUpperBound    int
	ArchiveSHA256            string
	PackageFingerprint       string
	ObjectKey                string
	DeliveredSHA256          string
	DeliveredBytes           int
	TokenUpperBound          int
	RedactionCount           int
	Content                  string
}

type ExternalContextAssembly struct {
	ProtocolVersion      string
	SelectionID          string
	RunID                string
	MissionID            string
	Surface              domain.ExecutionSurface
	Profile              domain.Profile
	SelectionFingerprint string
	Fingerprint          string
	TokenBudget          int
	TokenUpperBound      int
	ItemCount            int
	RedactionCount       int
	Items                []ExternalContextItem
}

type ExternalRootContextPreparationRequest struct {
	RunID                string
	MissionID            string
	RootAgentID          string
	SupervisorAttemptID  string
	Turn                 int
	SelectionID          string
	ProtocolVersion      string
	Surface              domain.ExecutionSurface
	Profile              domain.Profile
	SelectionFingerprint string
	ContextFingerprint   string
	ItemCount            int
	TokenBudget          int
	TokenUpperBound      int
	RedactionCount       int
}

type ExternalRootContextPreparation struct {
	ID string
	ExternalRootContextPreparationRequest
	PreparedAt time.Time
	Recovered  bool
}

type ExternalRootContextCommit struct {
	PreparationID       string
	RunID               string
	SupervisorAttemptID string
	ModelAttempt        int
	CommittedAt         time.Time
}

type ExternalSpecialistContextAssembly struct {
	ProtocolVersion            string
	ParentSelectionID          string
	ParentSelectionFingerprint string
	RunID                      string
	MissionID                  string
	AgentID                    string
	ParentAgentID              string
	AgentAttemptID             string
	Turn                       int64
	ModeSnapshotID             string
	ModeRevision               int64
	Surface                    domain.ExecutionSurface
	Profile                    domain.Profile
	AssignmentFingerprint      string
	Fingerprint                string
	TokenBudget                int
	TokenUpperBound            int
	ItemCount                  int
	RedactionCount             int
	Items                      []ExternalContextItem
}

type ExternalSpecialistContextPreparationRequest struct {
	RunID                      string
	MissionID                  string
	AgentID                    string
	ParentAgentID              string
	AgentAttemptID             string
	Turn                       int64
	ParentSelectionID          string
	ProtocolVersion            string
	ParentSelectionFingerprint string
	ModeSnapshotID             string
	ModeRevision               int64
	Surface                    domain.ExecutionSurface
	Profile                    domain.Profile
	AssignmentFingerprint      string
	ContextFingerprint         string
	ItemCount                  int
	TokenBudget                int
	TokenUpperBound            int
	RedactionCount             int
}

type ExternalSpecialistContextPreparation struct {
	ID string
	ExternalSpecialistContextPreparationRequest
	PreparedAt time.Time
	Recovered  bool
}

type ExternalSpecialistContextCommit struct {
	PreparationID  string
	RunID          string
	AgentAttemptID string
	ModelAttempt   int
	CommittedAt    time.Time
}

func AssembleExternalContext(ctx context.Context, selection ExternalSelection,
	loader PackageObjectLoader,
) (ExternalContextAssembly, error) {
	if loader == nil {
		return ExternalContextAssembly{}, errors.New("external Skill package object loader is required")
	}
	if err := selection.Validate(); err != nil {
		return ExternalContextAssembly{}, fmt.Errorf("invalid external Skill selection: %w", err)
	}
	items := make([]ExternalContextItem, 0, len(selection.Items))
	for _, selected := range selection.Items {
		item, err := loadExternalContextItem(ctx, selected, loader)
		if err != nil {
			return ExternalContextAssembly{}, err
		}
		item.Ordinal = selected.Ordinal
		items = append(items, item)
	}
	assembly := ExternalContextAssembly{
		ProtocolVersion: ExternalContextProtocolVersion,
		SelectionID:     selection.ID, RunID: selection.RunID, MissionID: selection.MissionID,
		Surface: selection.Surface, Profile: selection.Profile,
		SelectionFingerprint: selection.Fingerprint, TokenBudget: selection.TokenBudget,
		Items: items,
	}
	accumulateExternalContext(&assembly.TokenUpperBound, &assembly.RedactionCount, items)
	assembly.ItemCount = len(items)
	assembly.Fingerprint = ExternalContextFingerprint(assembly)
	if err := assembly.Validate(); err != nil {
		return ExternalContextAssembly{}, err
	}
	return CloneExternalContextAssembly(assembly), nil
}

func loadExternalContextItem(ctx context.Context, selected ExternalSelectionItem,
	loader PackageObjectLoader,
) (ExternalContextItem, error) {
	var manifest Manifest
	var content []byte
	if selected.Plugin != nil {
		selectedLoader, ok := loader.(interface {
			LoadSelectedSkill(context.Context, ExternalSelectionItem) (Manifest, []byte, error)
		})
		if !ok {
			return ExternalContextItem{}, errors.New("selected Plugin Skill reader is required")
		}
		var err error
		manifest, content, err = selectedLoader.LoadSelectedSkill(ctx, selected)
		if err != nil {
			return ExternalContextItem{}, err
		}
	} else {
		descriptor := PackageObjectDescriptor{
			ProtocolVersion: PackageObjectProtocolVersion, ArchiveSHA256: selected.ArchiveSHA256,
			PackageFingerprint: selected.PackageFingerprint, ArchiveBytes: selected.ArchiveBytes,
		}
		loaded, err := loader.Load(ctx, descriptor)
		if err != nil {
			return ExternalContextItem{}, fmt.Errorf("load selected external Skill %q: %w", FormatInstalledPackageRef(selected.Name, selected.Version), err)
		}
		if err := loaded.Validate(descriptor); err != nil {
			return ExternalContextItem{}, err
		}
		manifest, content = loaded.Manifest, loaded.Content
	}
	if err := ValidateExternalSelectionContent(manifest, content, nativeSelectionBinding(selected.Plugin)); err != nil {
		return ExternalContextItem{}, err
	}
	if manifest.Name != selected.Name || manifest.Version != selected.Version ||
		manifest.ContentSHA256 != selected.ContentSHA256 ||
		manifest.ContentBytes != selected.ContentBytes ||
		manifest.ContentTokenUpperBound != selected.TokenUpperBound ||
		len(manifest.ToolDependencies) != selected.ToolDependencyCount {
		return ExternalContextItem{}, errors.New("loaded external Skill does not match its pinned manifest")
	}
	redacted := redact.Text(string(content))
	delivered := []byte(redacted.Text)
	if err := validateContextContent(delivered); err != nil {
		return ExternalContextItem{}, fmt.Errorf("external Skill redacted context is invalid: %w", err)
	}
	if len(delivered) > selected.TokenUpperBound {
		return ExternalContextItem{}, errors.New("external Skill redacted context exceeds its pinned token bound")
	}
	redactionCount := 0
	for _, finding := range redacted.Findings {
		redactionCount += finding.Count
	}
	digest := sha256.Sum256(delivered)
	return ExternalContextItem{
		Plugin:                   clonePluginSkillBinding(selected.Plugin),
		InstallationID:           selected.InstallationID,
		InstallationFingerprint:  selected.InstallationFingerprint,
		InstallResultFingerprint: selected.InstallResultFingerprint,
		Name:                     selected.Name, Version: selected.Version,
		SourceSHA256: selected.ContentSHA256, SourceBytes: selected.ContentBytes,
		SourceTokenUpperBound: selected.TokenUpperBound,
		ArchiveSHA256:         selected.ArchiveSHA256,
		PackageFingerprint:    selected.PackageFingerprint, ObjectKey: selected.ObjectKey,
		DeliveredSHA256: hex.EncodeToString(digest[:]), DeliveredBytes: len(delivered),
		TokenUpperBound: len(delivered), RedactionCount: redactionCount,
		Content: redacted.Text,
	}, nil
}

func (a ExternalContextAssembly) Validate() error {
	if a.ProtocolVersion != ExternalContextProtocolVersion ||
		!validSelectionIdentity(a.SelectionID) || !validSelectionIdentity(a.RunID) ||
		!validSelectionIdentity(a.MissionID) || !a.Surface.Valid() ||
		!validSHA256(a.SelectionFingerprint) || !validSHA256(a.Fingerprint) {
		return errors.New("external Skill context provenance is invalid")
	}
	profile, err := domain.ParseProfile(string(a.Profile))
	if err != nil || profile != a.Profile ||
		(a.Surface == domain.ExecutionSurfaceCyber && a.Profile != domain.ProfileScript) {
		return errors.New("external Skill context surface or Profile is invalid")
	}
	if a.TokenBudget <= 0 || a.TokenBudget > MaxExternalSelectionTokenBudget ||
		a.TokenUpperBound <= 0 || a.TokenUpperBound > a.TokenBudget ||
		len(a.Items) == 0 || len(a.Items) > MaxExternalSelectionItems ||
		a.ItemCount != len(a.Items) || a.RedactionCount < 0 {
		return errors.New("external Skill context bounds are invalid")
	}
	if err := validateExternalContextItems(a.Items, a.TokenUpperBound, a.RedactionCount); err != nil {
		return err
	}
	if a.Fingerprint != ExternalContextFingerprint(a) {
		return errors.New("external Skill context fingerprint is invalid")
	}
	return nil
}

func validateExternalContextItems(items []ExternalContextItem, totalTokens,
	totalRedactions int,
) error {
	tokens, redactions := 0, 0
	previousRef := ""
	for index, item := range items {
		ref := ExternalSelectionItemRef(ExternalSelectionItem{Plugin: item.Plugin, InstallationID: item.InstallationID, Name: item.Name, Version: item.Version})
		content := []byte(item.Content)
		digest := sha256.Sum256(content)
		wantKey, keyErr := PackageObjectKey(item.ArchiveSHA256)
		objectValid := validSHA256(item.InstallResultFingerprint) && keyErr == nil && item.ObjectKey == wantKey
		if item.Plugin != nil {
			objectValid = item.Plugin.Validate() == nil && item.Plugin.Revision == item.ArchiveSHA256 && item.InstallResultFingerprint == "" && item.ObjectKey == ""
		}
		if item.Ordinal != index+1 || !validPackageIdentity(item.InstallationID) ||
			!validSHA256(item.InstallationFingerprint) ||
			!objectValid || !validExternalSelectionIdentity(item.Name, item.Version, nativeSelectionBinding(item.Plugin)) || !validSHA256(item.SourceSHA256) ||
			item.SourceBytes <= 0 || item.SourceBytes > MaxContentBytes ||
			item.SourceTokenUpperBound != item.SourceBytes ||
			item.DeliveredBytes <= 0 || item.DeliveredBytes != len(content) ||
			item.TokenUpperBound != item.DeliveredBytes ||
			item.TokenUpperBound > item.SourceTokenUpperBound || item.RedactionCount < 0 ||
			!validSHA256(item.PackageFingerprint) ||
			!validSHA256(item.DeliveredSHA256) ||
			item.DeliveredSHA256 != hex.EncodeToString(digest[:]) ||
			validateContextContent(content) != nil || (previousRef != "" && previousRef >= ref) {
			return fmt.Errorf("external Skill context item %d is invalid", index+1)
		}
		tokens += item.TokenUpperBound
		redactions += item.RedactionCount
		previousRef = ref
	}
	if tokens != totalTokens || redactions != totalRedactions {
		return errors.New("external Skill context aggregate accounting is inconsistent")
	}
	return nil
}

func ExternalContextFingerprint(a ExternalContextAssembly) string {
	parts := []string{ExternalContextProtocolVersion, a.SelectionID, a.RunID, a.MissionID,
		string(a.Surface), string(a.Profile), a.SelectionFingerprint,
		strconv.Itoa(a.TokenBudget), strconv.Itoa(len(a.Items))}
	return runmutation.Fingerprint(append(parts, externalContextFingerprintParts(a.Items)...)...)
}

func externalContextFingerprintParts(items []ExternalContextItem) []string {
	parts := make([]string, 0, len(items)*15)
	for _, item := range items {
		parts = append(parts, strconv.Itoa(item.Ordinal), item.InstallationID,
			item.InstallationFingerprint, item.InstallResultFingerprint,
			item.Name, item.Version, item.SourceSHA256, strconv.Itoa(item.SourceBytes),
			strconv.Itoa(item.SourceTokenUpperBound), item.ArchiveSHA256,
			item.PackageFingerprint, item.ObjectKey, item.DeliveredSHA256,
			strconv.Itoa(item.DeliveredBytes), strconv.Itoa(item.TokenUpperBound),
			strconv.Itoa(item.RedactionCount))
		parts = append(parts, pluginSelectionFingerprintParts(item.Plugin)...)
	}
	return parts
}

func (a ExternalContextAssembly) Preparation(rootAgentID, attemptID string,
	turn int,
) ExternalRootContextPreparationRequest {
	return ExternalRootContextPreparationRequest{
		RunID: a.RunID, MissionID: a.MissionID, RootAgentID: rootAgentID,
		SupervisorAttemptID: attemptID, Turn: turn, SelectionID: a.SelectionID,
		ProtocolVersion: a.ProtocolVersion, Surface: a.Surface, Profile: a.Profile,
		SelectionFingerprint: a.SelectionFingerprint, ContextFingerprint: a.Fingerprint,
		ItemCount: a.ItemCount, TokenBudget: a.TokenBudget,
		TokenUpperBound: a.TokenUpperBound, RedactionCount: a.RedactionCount,
	}
}

func (r ExternalRootContextPreparationRequest) Validate() error {
	for _, value := range []string{r.RunID, r.MissionID, r.RootAgentID,
		r.SupervisorAttemptID, r.SelectionID} {
		if !validSelectionIdentity(value) {
			return errors.New("external root Skill context identities are invalid")
		}
	}
	if r.Turn <= 0 || r.ProtocolVersion != ExternalContextProtocolVersion ||
		!r.Surface.Valid() || !validSHA256(r.SelectionFingerprint) ||
		!validSHA256(r.ContextFingerprint) || r.ItemCount <= 0 ||
		r.ItemCount > MaxExternalSelectionItems || r.TokenBudget <= 0 ||
		r.TokenBudget > MaxExternalSelectionTokenBudget || r.TokenUpperBound <= 0 ||
		r.TokenUpperBound > r.TokenBudget || r.RedactionCount < 0 {
		return errors.New("external root Skill context provenance or bounds are invalid")
	}
	profile, err := domain.ParseProfile(string(r.Profile))
	if err != nil || profile != r.Profile ||
		(r.Surface == domain.ExecutionSurfaceCyber && r.Profile != domain.ProfileScript) {
		return errors.New("external root Skill context surface or Profile is invalid")
	}
	return nil
}

func (p ExternalRootContextPreparation) Validate() error {
	if !validSelectionIdentity(p.ID) || !validUTC(p.PreparedAt) {
		return errors.New("external root Skill context preparation identity or time is invalid")
	}
	return p.ExternalRootContextPreparationRequest.Validate()
}

func (c ExternalRootContextCommit) Validate() error {
	if !validSelectionIdentity(c.PreparationID) || !validSelectionIdentity(c.RunID) ||
		!validSelectionIdentity(c.SupervisorAttemptID) || c.ModelAttempt <= 0 ||
		!validUTC(c.CommittedAt) {
		return errors.New("external root Skill context commit is invalid")
	}
	return nil
}

func AssembleExternalSpecialistContext(ctx context.Context, selection ExternalSelection,
	mode domain.RunModeSnapshot, child domain.AgentNode, attempt domain.AgentAttempt,
	loader PackageObjectLoader, tokenBudget int,
) (ExternalSpecialistContextAssembly, error) {
	if err := selection.Validate(); err != nil {
		return ExternalSpecialistContextAssembly{}, err
	}
	if err := mode.Validate(); err != nil {
		return ExternalSpecialistContextAssembly{}, err
	}
	if err := child.Validate(); err != nil {
		return ExternalSpecialistContextAssembly{}, err
	}
	if err := attempt.Validate(); err != nil {
		return ExternalSpecialistContextAssembly{}, err
	}
	selected, found := ExternalSpecialistItem(selection)
	if !found {
		return ExternalSpecialistContextAssembly{}, nil
	}
	if tokenBudget == 0 {
		tokenBudget = DefaultExternalSpecialistTokenBudget
	}
	if tokenBudget <= 0 || tokenBudget > MaxExternalSpecialistTokenBudget ||
		selected.TokenUpperBound > tokenBudget {
		return ExternalSpecialistContextAssembly{}, errors.New("external Specialist Skill context budget is insufficient or invalid")
	}
	if mode.RunID != selection.RunID || mode.MissionID != selection.MissionID ||
		mode.Surface != selection.Surface || mode.Profile != selection.Profile ||
		child.RunID != selection.RunID || child.Profile != selection.Profile ||
		attempt.RunID != selection.RunID || attempt.AgentID != child.ID ||
		attempt.ParentAgentID != child.ParentID {
		return ExternalSpecialistContextAssembly{}, errors.New("external Specialist Skill context provenance does not match the Run")
	}
	item, err := loadExternalContextItem(ctx, selected, loader)
	if err != nil {
		return ExternalSpecialistContextAssembly{}, err
	}
	item.Ordinal = 1
	assembly := ExternalSpecialistContextAssembly{
		ProtocolVersion:   ExternalSpecialistContextProtocolVersion,
		ParentSelectionID: selection.ID, ParentSelectionFingerprint: selection.Fingerprint,
		RunID: selection.RunID, MissionID: selection.MissionID, AgentID: child.ID,
		ParentAgentID: child.ParentID, AgentAttemptID: attempt.ID, Turn: attempt.Turn,
		ModeSnapshotID: mode.ID, ModeRevision: mode.Revision, Surface: mode.Surface,
		Profile: mode.Profile, AssignmentFingerprint: SpecialistAssignmentFingerprint(child),
		TokenBudget: tokenBudget, TokenUpperBound: item.TokenUpperBound,
		ItemCount: 1, RedactionCount: item.RedactionCount, Items: []ExternalContextItem{item},
	}
	assembly.Fingerprint = ExternalSpecialistContextFingerprint(assembly)
	if err := assembly.Validate(); err != nil {
		return ExternalSpecialistContextAssembly{}, err
	}
	return CloneExternalSpecialistContextAssembly(assembly), nil
}

func (a ExternalSpecialistContextAssembly) Validate() error {
	if a.ProtocolVersion == "" && a.ItemCount == 0 && len(a.Items) == 0 {
		return nil
	}
	for _, value := range []string{a.ParentSelectionID, a.RunID, a.MissionID,
		a.AgentID, a.ParentAgentID, a.AgentAttemptID, a.ModeSnapshotID} {
		if !validSelectionIdentity(value) {
			return errors.New("external Specialist Skill context identities are invalid")
		}
	}
	if a.ProtocolVersion != ExternalSpecialistContextProtocolVersion ||
		!validSHA256(a.ParentSelectionFingerprint) ||
		!validSHA256(a.AssignmentFingerprint) || !validSHA256(a.Fingerprint) ||
		a.Turn <= 0 || a.ModeRevision <= 0 || !a.Surface.Valid() ||
		a.TokenBudget <= 0 || a.TokenBudget > MaxExternalSpecialistTokenBudget ||
		a.ItemCount != 1 || len(a.Items) != 1 || a.TokenUpperBound <= 0 ||
		a.TokenUpperBound > a.TokenBudget || a.RedactionCount < 0 {
		return errors.New("external Specialist Skill context provenance or bounds are invalid")
	}
	profile, err := domain.ParseProfile(string(a.Profile))
	if err != nil || profile != a.Profile ||
		(a.Surface == domain.ExecutionSurfaceCyber && a.Profile != domain.ProfileScript) {
		return errors.New("external Specialist Skill context surface or Profile is invalid")
	}
	if err := validateExternalContextItems(a.Items, a.TokenUpperBound, a.RedactionCount); err != nil {
		return err
	}
	if a.Fingerprint != ExternalSpecialistContextFingerprint(a) {
		return errors.New("external Specialist Skill context fingerprint is invalid")
	}
	return nil
}

func ExternalSpecialistContextFingerprint(a ExternalSpecialistContextAssembly) string {
	parts := []string{ExternalSpecialistContextProtocolVersion, a.ParentSelectionID,
		a.ParentSelectionFingerprint, a.RunID, a.MissionID, a.AgentID, a.ParentAgentID,
		a.AgentAttemptID, strconv.FormatInt(a.Turn, 10), a.ModeSnapshotID,
		strconv.FormatInt(a.ModeRevision, 10), string(a.Surface), string(a.Profile),
		a.AssignmentFingerprint, strconv.Itoa(a.TokenBudget), strconv.Itoa(len(a.Items))}
	return runmutation.Fingerprint(append(parts, externalContextFingerprintParts(a.Items)...)...)
}

func (a ExternalSpecialistContextAssembly) Preparation() ExternalSpecialistContextPreparationRequest {
	return ExternalSpecialistContextPreparationRequest{
		RunID: a.RunID, MissionID: a.MissionID, AgentID: a.AgentID,
		ParentAgentID: a.ParentAgentID, AgentAttemptID: a.AgentAttemptID, Turn: a.Turn,
		ParentSelectionID: a.ParentSelectionID, ProtocolVersion: a.ProtocolVersion,
		ParentSelectionFingerprint: a.ParentSelectionFingerprint,
		ModeSnapshotID:             a.ModeSnapshotID, ModeRevision: a.ModeRevision,
		Surface: a.Surface, Profile: a.Profile,
		AssignmentFingerprint: a.AssignmentFingerprint,
		ContextFingerprint:    a.Fingerprint, ItemCount: a.ItemCount,
		TokenBudget: a.TokenBudget, TokenUpperBound: a.TokenUpperBound,
		RedactionCount: a.RedactionCount,
	}
}

func (r ExternalSpecialistContextPreparationRequest) Validate() error {
	assembly := ExternalSpecialistContextAssembly{
		ProtocolVersion: r.ProtocolVersion, ParentSelectionID: r.ParentSelectionID,
		ParentSelectionFingerprint: r.ParentSelectionFingerprint,
		RunID:                      r.RunID, MissionID: r.MissionID, AgentID: r.AgentID,
		ParentAgentID: r.ParentAgentID, AgentAttemptID: r.AgentAttemptID, Turn: r.Turn,
		ModeSnapshotID: r.ModeSnapshotID, ModeRevision: r.ModeRevision,
		Surface: r.Surface, Profile: r.Profile,
		AssignmentFingerprint: r.AssignmentFingerprint, Fingerprint: r.ContextFingerprint,
		TokenBudget: r.TokenBudget, TokenUpperBound: r.TokenUpperBound,
		ItemCount: r.ItemCount, RedactionCount: r.RedactionCount,
	}
	for _, value := range []string{assembly.ParentSelectionID, assembly.RunID,
		assembly.MissionID, assembly.AgentID, assembly.ParentAgentID,
		assembly.AgentAttemptID, assembly.ModeSnapshotID} {
		if !validSelectionIdentity(value) {
			return errors.New("external Specialist Skill context preparation identities are invalid")
		}
	}
	if assembly.ProtocolVersion != ExternalSpecialistContextProtocolVersion ||
		!validSHA256(assembly.ParentSelectionFingerprint) ||
		!validSHA256(assembly.AssignmentFingerprint) || !validSHA256(assembly.Fingerprint) ||
		assembly.Turn <= 0 || assembly.ModeRevision <= 0 || !assembly.Surface.Valid() ||
		assembly.TokenBudget <= 0 || assembly.TokenBudget > MaxExternalSpecialistTokenBudget ||
		assembly.ItemCount != 1 || assembly.TokenUpperBound <= 0 ||
		assembly.TokenUpperBound > assembly.TokenBudget || assembly.RedactionCount < 0 {
		return errors.New("external Specialist Skill context preparation provenance or bounds are invalid")
	}
	profile, err := domain.ParseProfile(string(assembly.Profile))
	if err != nil || profile != assembly.Profile ||
		(assembly.Surface == domain.ExecutionSurfaceCyber && assembly.Profile != domain.ProfileScript) {
		return errors.New("external Specialist Skill context preparation surface or Profile is invalid")
	}
	return nil
}

func (p ExternalSpecialistContextPreparation) Validate() error {
	if !validSelectionIdentity(p.ID) || !validUTC(p.PreparedAt) {
		return errors.New("external Specialist Skill context preparation identity or time is invalid")
	}
	return p.ExternalSpecialistContextPreparationRequest.Validate()
}

func (c ExternalSpecialistContextCommit) Validate() error {
	if !validSelectionIdentity(c.PreparationID) || !validSelectionIdentity(c.RunID) ||
		!validSelectionIdentity(c.AgentAttemptID) || c.ModelAttempt <= 0 ||
		!validUTC(c.CommittedAt) {
		return errors.New("external Specialist Skill context commit is invalid")
	}
	return nil
}

func CloneExternalContextAssembly(value ExternalContextAssembly) ExternalContextAssembly {
	value.Items = append([]ExternalContextItem(nil), value.Items...)
	for i := range value.Items {
		value.Items[i].Plugin = clonePluginSkillBinding(value.Items[i].Plugin)
	}
	return value
}

func CloneExternalSpecialistContextAssembly(
	value ExternalSpecialistContextAssembly,
) ExternalSpecialistContextAssembly {
	value.Items = append([]ExternalContextItem(nil), value.Items...)
	for i := range value.Items {
		value.Items[i].Plugin = clonePluginSkillBinding(value.Items[i].Plugin)
	}
	return value
}

func accumulateExternalContext(tokens, redactions *int, items []ExternalContextItem) {
	for _, item := range items {
		*tokens += item.TokenUpperBound
		*redactions += item.RedactionCount
	}
}
