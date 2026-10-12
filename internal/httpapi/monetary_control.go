package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/pricing"
)

const (
	PriceSnapshotsPath = "/api/v1/models/prices"

	PriceSnapshotListProtocol = "price_snapshot_list.v1"
)

// PriceSnapshotController is the bounded operator surface for price tables.
// Only the Go control plane accepts a document; a Provider response, README,
// Skill, or repository file can never reach it.
type PriceSnapshotController interface {
	ImportPriceSnapshot(context.Context, pricing.Snapshot) (pricing.Snapshot, bool, error)
	ListPriceSnapshots(context.Context, int) ([]pricing.Snapshot, error)
}

type PriceSnapshotImportRequestView struct {
	Version  string `json:"version"`
	Document string `json:"document"`
}

type PriceSnapshotImportView struct {
	ProtocolVersion string `json:"protocol_version"`
	ID              string `json:"id"`
	Currency        string `json:"currency"`
	Source          string `json:"source"`
	EntryCount      int    `json:"entry_count"`
	Fingerprint     string `json:"fingerprint"`
	Replayed        bool   `json:"replayed"`
}

type PriceSnapshotItemView struct {
	ID          string           `json:"id"`
	Source      string           `json:"source"`
	Currency    string           `json:"currency"`
	ImportedBy  string           `json:"imported_by"`
	ImportedAt  time.Time        `json:"imported_at"`
	ValidFrom   time.Time        `json:"valid_from"`
	ValidUntil  time.Time        `json:"valid_until"`
	Fingerprint string           `json:"fingerprint"`
	EntryCount  int              `json:"entry_count"`
	Entries     []PriceEntryView `json:"entries"`
}

type PriceEntryView struct {
	Provider                        string `json:"provider"`
	Model                           string `json:"model"`
	InputPerMillionMicros           int64  `json:"input_per_million_micros"`
	OutputPerMillionMicros          int64  `json:"output_per_million_micros"`
	CacheReadPerMillionMicros       *int64 `json:"cache_read_per_million_micros,omitempty"`
	CacheCreationPerMillionMicros   *int64 `json:"cache_creation_per_million_micros,omitempty"`
	CacheCreation5mPerMillionMicros *int64 `json:"cache_creation_5m_per_million_micros,omitempty"`
	CacheCreation1hPerMillionMicros *int64 `json:"cache_creation_1h_per_million_micros,omitempty"`
	ToolCallMicros                  int64  `json:"tool_call_micros"`
}

type PriceSnapshotListView struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Items           []PriceSnapshotItemView `json:"items"`
}

func (a *API) servePriceSnapshots(writer http.ResponseWriter, request *http.Request,
	requestID string,
) {
	if a.priceSnapshotController == nil {
		a.writeError(writer, requestID,
			apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"),
			http.StatusNotFound)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"price snapshot endpoint only supports GET and POST"), http.StatusMethodNotAllowed)
		return
	}
	switch request.Method {
	case http.MethodGet:
		if !a.authorized(request, a.tokenHash) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
			a.writeError(writer, requestID,
				apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"),
				http.StatusUnauthorized)
			return
		}
		if err := rejectQuery(request.URL.Query()); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		items, err := a.priceSnapshotController.ListPriceSnapshots(request.Context(), 32)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		view := PriceSnapshotListView{ProtocolVersion: PriceSnapshotListProtocol,
			Items: []PriceSnapshotItemView{}}
		for _, snapshot := range items {
			item := PriceSnapshotItemView{
				ID: snapshot.ID, Source: snapshot.Source, Currency: snapshot.Currency,
				ImportedBy: snapshot.ImportedBy, ImportedAt: snapshot.ImportedAt,
				ValidFrom: snapshot.ValidFrom, ValidUntil: snapshot.ValidUntil,
				Fingerprint: snapshot.Fingerprint, EntryCount: len(snapshot.Entries),
			}
			for _, entry := range snapshot.Entries {
				item.Entries = append(item.Entries, PriceEntryView{
					Provider: entry.Provider, Model: entry.Model,
					InputPerMillionMicros:           entry.InputPerMillionMicros,
					OutputPerMillionMicros:          entry.OutputPerMillionMicros,
					CacheReadPerMillionMicros:       entry.CacheReadPerMillionMicros,
					CacheCreationPerMillionMicros:   entry.CacheCreationPerMillionMicros,
					CacheCreation5mPerMillionMicros: entry.CacheCreation5mPerMillionMicros,
					CacheCreation1hPerMillionMicros: entry.CacheCreation1hPerMillionMicros,
					ToolCallMicros:                  entry.ToolCallMicros,
				})
			}
			view.Items = append(view.Items, item)
		}
		a.writeSuccessStatus(writer, requestID, view, nil, http.StatusOK)
	case http.MethodPost:
		if !a.authorizeRunOperation(writer, request, requestID, a.modelControlEnabled,
			"Price snapshot import") {
			return
		}
		strictBody, err := readStrictControlBody(request, "Price snapshot import")
		if err != nil {
			a.writeError(writer, requestID, err, runOperationErrorStatus(err))
			return
		}
		var body PriceSnapshotImportRequestView
		if err := decodeStrictRunOperation(strictBody, &body, "Price snapshot import"); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if body.Version != pricing.ProtocolVersion ||
			len(body.Document) == 0 || len(body.Document) > pricing.MaxSnapshotBytes ||
			strings.TrimSpace(body.Document) != body.Document ||
			!json.Valid([]byte(body.Document)) {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"price snapshot import document is invalid"), http.StatusBadRequest)
			return
		}
		snapshot, err := pricing.ParseWire([]byte(body.Document), time.Now().UTC())
		if err != nil {
			a.writeError(writer, requestID, apperror.Wrap(apperror.CodeInvalidArgument,
				"price snapshot document was rejected", err), http.StatusBadRequest)
			return
		}
		stored, replayed, err := a.priceSnapshotController.ImportPriceSnapshot(
			request.Context(), snapshot)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, PriceSnapshotImportView{
			ProtocolVersion: stored.ProtocolVersion, ID: stored.ID,
			Currency: stored.Currency, Source: stored.Source,
			EntryCount: len(stored.Entries), Fingerprint: stored.Fingerprint,
			Replayed: replayed,
		}, nil, http.StatusOK)
	default:
		writer.Header().Set("Allow", "GET, POST")
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"price snapshot endpoint only supports GET and POST"), http.StatusMethodNotAllowed)
	}
}
