package httpapi

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/skills"
)

const SkillCatalogPublishersPath = "/api/v1/skills/catalog/publishers"

type SkillCatalogController interface {
	ListPublishers(context.Context) ([]skills.CatalogPublisher, error)
	ControlPublisher(context.Context, application.SkillCatalogPublisherControlRequest) (skills.CatalogPublisher, error)
}

type SkillCatalogPublisherControlRequestView struct {
	Version           string  `json:"version"`
	Action            string  `json:"action"`
	ExpectedUpdatedAt *string `json:"expected_updated_at"`
	Fingerprint       string  `json:"fingerprint"`
	PublicKey         string  `json:"public_key,omitempty"`
	Name              string  `json:"name,omitempty"`
	Team              string  `json:"team,omitempty"`
	Confirm           bool    `json:"confirm"`
}

type SkillCatalogPublisherView struct {
	Fingerprint string     `json:"fingerprint"`
	Name        string     `json:"name"`
	Team        string     `json:"team"`
	PublicKey   string     `json:"public_key"`
	TrustClass  string     `json:"trust_class"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CreatedAt   time.Time  `json:"created_at"`
	TrustedAt   *time.Time `json:"trusted_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

type SkillCatalogPublishersView struct {
	ProtocolVersion string                      `json:"protocol_version"`
	Publishers      []SkillCatalogPublisherView `json:"publishers"`
	ControlEnabled  bool                        `json:"control_enabled"`
}

func skillCatalogPublisherView(p skills.CatalogPublisher) SkillCatalogPublisherView {
	// Legacy CLI entries may contain line-wrapped Base64. Project the same key
	// canonically without changing the stored identity or its observed version.
	if key, err := base64.StdEncoding.DecodeString(p.PublicKey); err == nil && len(key) == ed25519.PublicKeySize {
		p.PublicKey = base64.StdEncoding.EncodeToString(key)
	}
	return SkillCatalogPublisherView{Fingerprint: p.Fingerprint, Name: p.Name, Team: p.Team, PublicKey: p.PublicKey, TrustClass: p.TrustClass, UpdatedAt: p.UpdatedAt, CreatedAt: p.CreatedAt, TrustedAt: p.TrustedAt, RevokedAt: p.RevokedAt}
}

func (a *API) serveSkillCatalogPublishers(w http.ResponseWriter, r *http.Request, requestID string) {
	if a.skillCatalogController == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Skill catalog is unavailable"), http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		if !a.authorized(r, a.tokenHash) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
			a.writeError(w, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if err := rejectQuery(r.URL.Query()); err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Skill catalog GET cannot contain a body"), 0)
			return
		}
		publishers, err := a.skillCatalogController.ListPublishers(r.Context())
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
		view := SkillCatalogPublishersView{ProtocolVersion: application.SkillCatalogControlProtocol, ControlEnabled: a.skillCatalogControlEnabled, Publishers: []SkillCatalogPublisherView{}}
		for _, publisher := range publishers {
			view.Publishers = append(view.Publishers, skillCatalogPublisherView(publisher))
		}
		a.writeSuccess(w, requestID, view, nil)
		return
	}
	const label = "Skill catalog publisher control"
	if !a.authorizeRunOperation(w, r, requestID, a.skillCatalogControlEnabled, label) {
		return
	}
	body, err := readStrictControlBody(r, label)
	if err != nil {
		a.writeError(w, requestID, err, runOperationErrorStatus(err))
		return
	}
	var view SkillCatalogPublisherControlRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if view.Version != application.SkillCatalogControlProtocol || view.ExpectedUpdatedAt == nil {
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "Skill catalog control version and observed updated_at are required"), 0)
		return
	}
	value, err := a.skillCatalogController.ControlPublisher(r.Context(), application.SkillCatalogPublisherControlRequest{
		Action: view.Action, ExpectedUpdatedAt: *view.ExpectedUpdatedAt, Fingerprint: view.Fingerprint, PublicKey: view.PublicKey, Name: view.Name, Team: view.Team, Confirm: view.Confirm, Actor: "http_operator",
	})
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, skillCatalogPublisherView(value), nil)
}
