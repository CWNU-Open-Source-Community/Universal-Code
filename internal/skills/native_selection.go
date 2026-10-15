package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/toolgateway"
)

// ExternalSelectionItemRef preserves historical name@version identities while
// native Skills use the same exact component pin as skill_read. Author versions
// may be absent and are never replaced with a synthetic semantic version.
func ExternalSelectionItemRef(item ExternalSelectionItem) string {
	if item.Plugin == nil || !item.Plugin.Native {
		return FormatInstalledPackageRef(item.Name, item.Version)
	}
	encoded, _ := json.Marshal(toolgateway.SkillReadRequest{
		InstallationID: item.InstallationID, PackageID: item.Plugin.PackageID,
		ComponentID: item.Plugin.ComponentID, Revision: item.Plugin.Revision,
		InstallationGeneration: item.Plugin.Generation,
	})
	return string(encoded)
}

func validExternalSelectionIdentity(name, version string, native bool) bool {
	if !native {
		return validName(name) && validCoreVersion(version)
	}
	// Match the portable Agent Skills name contract. Native plugin author
	// versions are optional free text, bounded by the acquired manifest size.
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 ||
		strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") ||
		!utf8.ValidString(version) || len(version) > 256*1024 {
		return false
	}
	for _, r := range name {
		if r != '-' && ((!unicode.IsLetter(r) && !unicode.IsNumber(r)) || unicode.ToLower(r) != r) {
			return false
		}
	}
	return strings.IndexFunc(version, unicode.IsControl) < 0
}

func nativeSelectionBinding(binding *PluginSkillBinding) bool {
	return binding != nil && binding.Native
}

func validNativeSelectionRef(ref string) bool {
	request, canonical, err := toolgateway.NormalizeSkillReadPayload(json.RawMessage(ref))
	return err == nil && request.Portable() && request.Resource == "" && string(canonical) == ref
}

// ValidateExternalSelectionContent is shared by candidate admission and actual
// context delivery. Native metadata is already parsed from an acquired snapshot;
// its body still obeys the existing untrusted-context byte/token/content limits.
func ValidateExternalSelectionContent(manifest Manifest, body []byte, native bool) error {
	if !native {
		return manifest.Validate(body)
	}
	if !validExternalSelectionIdentity(manifest.Name, manifest.Version, true) ||
		validateProfiles(manifest.Profiles) != nil || validateModeMetadata(manifest) != nil ||
		len(manifest.ToolDependencies) != 0 || validateContextContent(body) != nil {
		return errors.New("native Skill metadata or context content is invalid")
	}
	digest := sha256.Sum256(body)
	if manifest.ContentSHA256 != hex.EncodeToString(digest[:]) || manifest.ContentBytes != len(body) ||
		manifest.ContentTokenUpperBound != ContentTokenUpperBound(body) ||
		manifest.ContentTokenUpperBound > MaxExternalSelectionTokenBudget {
		return errors.New("native Skill content digest or token bound is invalid")
	}
	return nil
}
