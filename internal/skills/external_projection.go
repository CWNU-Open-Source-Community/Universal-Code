package skills

import (
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/domain"
)

const ExternalSkillProjectionProtocolVersion = "external_skill_projection.v1"

// ExternalSkillProjectionItem is the deliberately small public description of
// one Run-pinned external Skill. Package content, storage identity, and digests
// are not representable by this type.
type ExternalSkillProjectionItem struct {
	// Internal validation fields are not part of the HTTP projection.
	Native             bool   `json:"-"`
	SelectionRef       string `json:"-"`
	Ordinal            int
	Name               string
	Version            string
	TokenUpperBound    int
	TrustClass         PackageTrustClass
	DeclaredToolCount  int
	SpecialistEligible bool
}

// ExternalSkillProjection exposes only bounded selection and delivery
// provenance. It carries no package body, path, digest, installation identity,
// requester identity, or operation identity.
type ExternalSkillProjection struct {
	ProtocolVersion           string
	RunID                     string
	ModeRevision              int64
	Surface                   domain.ExecutionSurface
	Profile                   domain.Profile
	TokenBudget               int
	TokenUpperBound           int
	ItemCount                 int
	OperatorConfirmed         bool
	ContextDeliveryAuthorized bool
	ToolCapabilityGrant       bool
	RootPreparedCount         int
	RootCommittedCount        int
	SpecialistPreparedCount   int
	SpecialistCommittedCount  int
	Items                     []ExternalSkillProjectionItem
	CreatedAt                 time.Time
}

func (p ExternalSkillProjection) Validate() error {
	if p.ProtocolVersion != ExternalSkillProjectionProtocolVersion ||
		!validSelectionIdentity(p.RunID) || p.ModeRevision <= 0 || !p.Surface.Valid() ||
		!p.OperatorConfirmed || !p.ContextDeliveryAuthorized || p.ToolCapabilityGrant ||
		!validUTC(p.CreatedAt) {
		return errors.New("external Skill projection protocol or capability boundary is invalid")
	}
	profile, err := domain.ParseProfile(string(p.Profile))
	if err != nil || profile != p.Profile ||
		(p.Surface == domain.ExecutionSurfaceCyber && p.Profile != domain.ProfileScript) {
		return errors.New("external Skill projection surface or Profile is invalid")
	}
	if p.TokenBudget <= 0 || p.TokenBudget > MaxExternalSelectionTokenBudget ||
		p.TokenUpperBound <= 0 || p.TokenUpperBound > p.TokenBudget ||
		len(p.Items) == 0 || len(p.Items) > MaxExternalSelectionItems ||
		p.ItemCount != len(p.Items) {
		return errors.New("external Skill projection bounds are invalid")
	}
	if p.RootPreparedCount < 0 || p.RootCommittedCount < 0 ||
		p.RootCommittedCount > p.RootPreparedCount || p.SpecialistPreparedCount < 0 ||
		p.SpecialistCommittedCount < 0 ||
		p.SpecialistCommittedCount > p.SpecialistPreparedCount {
		return errors.New("external Skill projection provenance counts are invalid")
	}
	total := 0
	previousRef := ""
	specialistCount := 0
	for index, item := range p.Items {
		ref := FormatInstalledPackageRef(item.Name, item.Version)
		if item.Native {
			ref = item.SelectionRef
			if !validNativeSelectionRef(ref) {
				return fmt.Errorf("external Skill projection item %d native reference is invalid", index+1)
			}
		}
		if item.Ordinal != index+1 || !validExternalSelectionIdentity(item.Name, item.Version, item.Native) || item.TokenUpperBound <= 0 ||
			item.TokenUpperBound > MaxContentTokenUpperBound ||
			item.TrustClass != PackageTrustOperatorInstalledUntrusted ||
			item.DeclaredToolCount < 0 || item.DeclaredToolCount > MaxToolDependencies ||
			(previousRef != "" && previousRef >= ref) {
			return fmt.Errorf("external Skill projection item %d is invalid", index+1)
		}
		if item.SpecialistEligible {
			if item.TokenUpperBound > MaxExternalSpecialistTokenBudget {
				return fmt.Errorf("external Skill projection item %d exceeds the Specialist hard limit", index+1)
			}
			specialistCount++
		}
		total += item.TokenUpperBound
		previousRef = ref
	}
	if total != p.TokenUpperBound || specialistCount > 1 {
		return errors.New("external Skill projection accounting is invalid")
	}
	return nil
}

func CloneExternalSkillProjection(value ExternalSkillProjection) ExternalSkillProjection {
	value.Items = append([]ExternalSkillProjectionItem(nil), value.Items...)
	return value
}
