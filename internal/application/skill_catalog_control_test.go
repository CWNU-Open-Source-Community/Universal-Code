package application

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/skills"
)

func TestSkillCatalogControlValidatesObservedIdentityAndConfirmation(t *testing.T) {
	service, _, _ := newCatalogFixture(t)
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	encoded := base64.StdEncoding.EncodeToString(key)
	fingerprint, _ := skills.PublisherFingerprintFromBase64(encoded)
	valid := SkillCatalogPublisherControlRequest{Action: "trust", Fingerprint: fingerprint, PublicKey: encoded, Name: "Publisher", Confirm: true, Actor: "web"}
	for _, tc := range []struct {
		name   string
		change func(*SkillCatalogPublisherControlRequest)
	}{
		{"unconfirmed", func(r *SkillCatalogPublisherControlRequest) { r.Confirm = false }},
		{"wrong fingerprint", func(r *SkillCatalogPublisherControlRequest) { r.Fingerprint = strings.Repeat("f", 64) }},
		{"malformed key", func(r *SkillCatalogPublisherControlRequest) { r.PublicKey = "invalid" }},
		{"team too long", func(r *SkillCatalogPublisherControlRequest) { r.Team = strings.Repeat("界", 129) }},
		{"invalid UTF-8 team", func(r *SkillCatalogPublisherControlRequest) { r.Team = string([]byte{0xff}) }},
		{"noncanonical version", func(r *SkillCatalogPublisherControlRequest) { r.ExpectedUpdatedAt = "2026-01-01T00:00:00+00:00" }},
		{"missing revoke observation", func(r *SkillCatalogPublisherControlRequest) { r.Action = "revoke"; r.PublicKey = ""; r.Name = "" }},
		{"unknown action", func(r *SkillCatalogPublisherControlRequest) { r.Action = "enable" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.change(&r)
			if _, err := service.ControlPublisher(t.Context(), r); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
				t.Fatalf("invalid decision=%v", err)
			}
		})
	}
	publishers, err := service.ListPublishers(t.Context())
	if err != nil || len(publishers) != 0 {
		t.Fatalf("invalid decisions wrote state: %+v %v", publishers, err)
	}
	value, err := service.ControlPublisher(t.Context(), valid)
	if err != nil || value.Fingerprint != fingerprint || value.TrustClass != "trusted" {
		t.Fatalf("valid trust=%+v %v", value, err)
	}
	if err := service.RevokePublisher(t.Context(), RevokeSkillPublisherRequest{Fingerprint: fingerprint, Actor: "cli"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ControlPublisher(t.Context(), valid); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("CLI revoke bypassed: %v", err)
	}
}

func TestSkillCatalogControlRegrantsLegacyCLITeamAtStoreLimit(t *testing.T) {
	for _, tc := range []struct{ name, unit string }{{"ASCII", "t"}, {"CJK", "界"}, {"supplementary", "😀"}} {
		t.Run(tc.name, func(t *testing.T) {
			team := strings.Repeat(tc.unit, 128)
			service, _, _ := newCatalogFixture(t)
			key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
			legacy, err := service.TrustPublisher(t.Context(), TrustSkillPublisherRequest{
				Name: "Legacy publisher", Team: team, PublicKey: base64.StdEncoding.EncodeToString(key), Actor: "cli",
			})
			if err != nil {
				t.Fatal(err)
			}
			revoked, err := service.ControlPublisher(t.Context(), SkillCatalogPublisherControlRequest{
				Action: "revoke", ExpectedUpdatedAt: legacy.UpdatedAt.Format(time.RFC3339Nano), Fingerprint: legacy.Fingerprint, Confirm: true, Actor: "web",
			})
			if err != nil {
				t.Fatal(err)
			}
			regranted, err := service.ControlPublisher(t.Context(), SkillCatalogPublisherControlRequest{
				Action: "trust", ExpectedUpdatedAt: revoked.UpdatedAt.Format(time.RFC3339Nano), Fingerprint: legacy.Fingerprint,
				PublicKey: legacy.PublicKey, Name: legacy.Name, Team: legacy.Team, Confirm: true, Actor: "web",
			})
			if err != nil || regranted.TrustClass != "trusted" || regranted.Team != team {
				t.Fatalf("legacy team regrant=%+v err=%v", regranted, err)
			}
		})
	}
}
