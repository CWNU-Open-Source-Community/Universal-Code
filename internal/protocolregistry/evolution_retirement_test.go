package protocolregistry

import (
	"sort"
	"strings"
	"testing"
)

func TestEvolutionPreservesReviewedIdentifierRetirement(t *testing.T) {
	previous := loadRepositoryRegistry(t, repositoryRoot(t))
	index := -1
	for i, family := range previous.Families {
		if len(family.RetiredIdentifiers) > 0 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("repository registry has no retirement fixture")
	}
	if err := ValidateEvolution(previous, cloneRegistry(t, previous)); err != nil {
		t.Fatalf("unchanged retirement rejected: %v", err)
	}
	for _, mode := range []string{"reactivate", "remove", "rewrite"} {
		t.Run(mode, func(t *testing.T) {
			current := cloneRegistry(t, previous)
			family := &current.Families[index]
			retired := family.RetiredIdentifiers[0]
			want := "deleted historical retirement"
			switch mode {
			case "reactivate":
				family.ActiveIdentifiers = append(family.ActiveIdentifiers, retired.Identifier)
				sort.Strings(family.ActiveIdentifiers)
				family.RetiredIdentifiers = family.RetiredIdentifiers[1:]
				if err := ValidateStructure(current); err != nil {
					t.Fatalf("reactivation fixture is structurally invalid: %v", err)
				}
			case "remove":
				family.RetiredIdentifiers = family.RetiredIdentifiers[1:]
			case "rewrite":
				family.RetiredIdentifiers[0].Decision = "replacement decision erases original history"
				want = "rewrote historical retirement"
			}
			if err := ValidateEvolution(previous, current); err == nil ||
				!strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), retired.Identifier) {
				t.Fatalf("%s retirement %s accepted: %v", mode, retired.Identifier, err)
			}
		})
	}
}
