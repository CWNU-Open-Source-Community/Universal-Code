package application

import (
	"context"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/skills"
)

const SkillCatalogControlProtocol = "skill-catalog-control.v1"

type SkillCatalogPublisherControlRequest struct {
	Action            string
	ExpectedUpdatedAt string
	Fingerprint       string
	PublicKey         string
	Name              string
	Team              string
	Confirm           bool
	Actor             string
}

func (s *SkillCatalogService) ControlPublisher(ctx context.Context, request SkillCatalogPublisherControlRequest) (skills.CatalogPublisher, error) {
	if s == nil || s.store == nil {
		return skills.CatalogPublisher{}, apperror.New(apperror.CodeFailedPrecondition, "Skill catalog service is not configured")
	}
	invalid := func() (skills.CatalogPublisher, error) {
		return skills.CatalogPublisher{}, apperror.New(apperror.CodeInvalidArgument, "Skill catalog publisher decision is invalid")
	}
	decoded, err := hex.DecodeString(request.Fingerprint)
	if err != nil || len(decoded) != 32 || strings.ToLower(request.Fingerprint) != request.Fingerprint || !request.Confirm || strings.TrimSpace(request.Actor) == "" {
		return invalid()
	}
	if request.ExpectedUpdatedAt != "" {
		stamp, err := time.Parse(time.RFC3339Nano, request.ExpectedUpdatedAt)
		if err != nil || stamp.IsZero() || stamp.UTC().Format(time.RFC3339Nano) != request.ExpectedUpdatedAt {
			return invalid()
		}
	}
	publisher := skills.CatalogPublisher{Fingerprint: request.Fingerprint}
	switch request.Action {
	case "trust":
		request.Name = strings.TrimSpace(request.Name)
		request.Team = strings.TrimSpace(request.Team)
		if request.Name == "" || len(request.Name) > 96 || !utf8.ValidString(request.Name) || !utf8.ValidString(request.Team) || utf8.RuneCountInString(request.Team) > 128 {
			return invalid()
		}
		fingerprint, err := skills.PublisherFingerprintFromBase64(request.PublicKey)
		if err != nil || fingerprint != request.Fingerprint {
			return invalid()
		}
		publisher.Name = request.Name
		publisher.Team = request.Team
		publisher.PublicKey = request.PublicKey
	case "revoke":
		if request.ExpectedUpdatedAt == "" || request.PublicKey != "" || request.Name != "" || request.Team != "" {
			return invalid()
		}
	default:
		return invalid()
	}
	value, err := s.store.CompareAndSetSkillCatalogPublisher(ctx, publisher, request.Action, request.ExpectedUpdatedAt, request.Actor)
	return value, apperror.Normalize(err)
}
