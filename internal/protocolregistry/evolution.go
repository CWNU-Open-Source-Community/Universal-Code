package protocolregistry

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

func ValidateEvolution(previous, current Registry) error {
	var problems []string
	if missing := removedStrings(previous.Scan.Roots, current.Scan.Roots); len(missing) != 0 {
		problems = append(problems, "scan roots were removed: "+strings.Join(missing, ", "))
	}
	if missing := removedStrings(previous.Scan.Extensions, current.Scan.Extensions); len(missing) != 0 {
		problems = append(problems, "scan extensions were removed: "+strings.Join(missing, ", "))
	}
	currentFamilies := make(map[string]Family, len(current.Families))
	currentClaims := make(map[string]string)
	for _, family := range current.Families {
		currentFamilies[family.ID] = family
		for _, identifier := range family.ActiveIdentifiers {
			currentClaims[identifier] = family.ID
		}
		for _, retired := range family.RetiredIdentifiers {
			currentClaims[retired.Identifier] = family.ID
		}
	}
	for _, oldFamily := range previous.Families {
		newFamily, ok := currentFamilies[oldFamily.ID]
		if !ok {
			problems = append(problems, fmt.Sprintf("family %q was deleted; retain its versioned history", oldFamily.ID))
			continue
		}
		if newFamily.Class != oldFamily.Class {
			problems = append(problems, fmt.Sprintf("family %q changed class from %s to %s without a superseding registry schema", oldFamily.ID, oldFamily.Class, newFamily.Class))
		}
		newRetired := make(map[string]RetiredIdentifier)
		for _, retired := range newFamily.RetiredIdentifiers {
			newRetired[retired.Identifier] = retired
		}
		for _, identifier := range oldFamily.ActiveIdentifiers {
			if owner, ok := currentClaims[identifier]; !ok {
				problems = append(problems, fmt.Sprintf("family %q deleted active identifier %q instead of retaining a retirement record", oldFamily.ID, identifier))
			} else if owner != oldFamily.ID {
				problems = append(problems, fmt.Sprintf("identifier %q moved from family %q to %q", identifier, oldFamily.ID, owner))
			} else if retired, ok := newRetired[identifier]; ok {
				if err := validateRetirementTransition(newFamily.Class, retired); err != nil {
					problems = append(problems, fmt.Sprintf("identifier %q retirement: %v", identifier, err))
				}
			}
		}
		for _, retired := range oldFamily.RetiredIdentifiers {
			if current, ok := newRetired[retired.Identifier]; !ok {
				problems = append(problems, fmt.Sprintf("family %q deleted historical retirement %q", oldFamily.ID, retired.Identifier))
			} else if current != retired {
				problems = append(problems, fmt.Sprintf("family %q rewrote historical retirement %q", oldFamily.ID, retired.Identifier))
			}
		}
		newReaders := make(map[string]Reader)
		for _, reader := range newFamily.Readers {
			newReaders[reader.ID] = reader
		}
		for _, oldReader := range oldFamily.Readers {
			newReader, ok := newReaders[oldReader.ID]
			if !ok {
				problems = append(problems, fmt.Sprintf("family %q deleted reader history %q", oldFamily.ID, oldReader.ID))
				continue
			}
			if missing := removedInts(oldReader.Versions, newReader.Versions); len(missing) != 0 {
				problems = append(problems, fmt.Sprintf("family %q reader %q dropped versions %v", oldFamily.ID, oldReader.ID, missing))
			}
			if oldReader.Status == ReaderRetired && newReader.Status != ReaderRetired {
				problems = append(problems, fmt.Sprintf("family %q reader %q erased its retired state", oldFamily.ID, oldReader.ID))
			}
			if oldReader.Status == ReaderActive && newReader.Status == ReaderRetired {
				if newReader.Retirement == nil || strings.TrimSpace(newReader.Retirement.Decision) == "" ||
					strings.TrimSpace(newReader.Retirement.Rollback) == "" ||
					((oldFamily.Class == ClassExternal || oldFamily.Class == ClassInternal) && strings.TrimSpace(newReader.Retirement.MigrationEvidence) == "") {
					problems = append(problems, fmt.Sprintf("family %q reader %q retirement lacks decision, migration/retention evidence, or rollback", oldFamily.ID, oldReader.ID))
				}
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return errors.New("protocol registry history violation:\n- " + strings.Join(problems, "\n- "))
}

func validateRetirementTransition(class string, retired RetiredIdentifier) error {
	if strings.TrimSpace(retired.Decision) == "" || strings.TrimSpace(retired.Rollback) == "" {
		return errors.New("decision and rollback are required")
	}
	switch class {
	case ClassExternal, ClassInternal:
		if strings.TrimSpace(retired.MigrationEvidence) == "" {
			return errors.New("durable reader migration or retention evidence is required")
		}
	case ClassProjection:
		if strings.TrimSpace(retired.RebuildEvidence) == "" {
			return errors.New("projection rebuild evidence is required")
		}
	case ClassEphemeral:
		if strings.TrimSpace(retired.RestartEvidence) == "" {
			return errors.New("restart non-persistence evidence is required")
		}
	}
	return nil
}

func removedStrings(previous, current []string) []string {
	set := make(map[string]struct{}, len(current))
	for _, value := range current {
		set[value] = struct{}{}
	}
	var missing []string
	for _, value := range previous {
		if _, ok := set[value]; !ok {
			missing = append(missing, value)
		}
	}
	return missing
}

func removedInts(previous, current []int) []int {
	set := make(map[int]struct{}, len(current))
	for _, value := range current {
		set[value] = struct{}{}
	}
	var missing []int
	for _, value := range previous {
		if _, ok := set[value]; !ok {
			missing = append(missing, value)
		}
	}
	return missing
}
