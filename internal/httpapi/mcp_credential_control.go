package httpapi

import (
	"context"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
)

const MCPCredentialPathTemplate = "/api/v1/extensions/mcp/{server_id}/credential"

type MCPCredentialController interface {
	HasMCPCredentialControl() bool
	MCPCredentialStatus(context.Context, application.MCPCredentialBinding) (application.MCPCredentialStatus, error)
	ChangeMCPCredential(context.Context, application.ChangeMCPCredentialRequest) (application.MCPCredentialStatus, error)
}

type MCPCredentialBindingView application.MCPCredentialBinding
type MCPCredentialStatusView application.MCPCredentialStatus
type MCPCredentialRequestView struct {
	Version                      string                   `json:"version"`
	Binding                      MCPCredentialBindingView `json:"binding"`
	Action                       string                   `json:"action"`
	OAuthFlowID                  string                   `json:"oauth_flow_id,omitempty"`
	OAuthIssuer                  string                   `json:"oauth_issuer,omitempty"`
	OAuthClientID                string                   `json:"oauth_client_id,omitempty"`
	OAuthClientIDMode            string                   `json:"oauth_client_id_mode,omitempty"`
	OAuthClientSecret            string                   `json:"oauth_client_secret,omitempty"`
	OAuthCallbackPort            int                      `json:"oauth_callback_port,omitempty"`
	Secret                       string                   `json:"secret,omitempty"`
	Confirm                      bool                     `json:"confirm"`
	ExpectedReferenceFingerprint string                   `json:"expected_reference_fingerprint"`
}

func matchMCPCredentialPath(path string) (string, bool) {
	const prefix, suffix = "/api/v1/extensions/mcp/", "/credential"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	identity := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return identity, identity != "" && !strings.Contains(identity, "/")
}

func (a *API) serveMCPCredential(writer http.ResponseWriter, request *http.Request, requestID, serverID string) {
	const label = "MCP credential"
	controller, ok := a.extensionController.(MCPCredentialController)
	if request.Method != http.MethodGet {
		if !a.authorizeRunOperation(writer, request, requestID, a.extensionControlEnabled, label) {
			return
		}
	} else {
		if !a.authorized(request, a.tokenHash) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
			a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "MCP credential presence requests cannot contain a body"), 0)
			return
		}
	}
	if !ok || controller == nil || !controller.HasMCPCredentialControl() {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "MCP credential management is unavailable"), http.StatusNotFound)
		return
	}
	if err := validatePathIdentity(serverID); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var value application.MCPCredentialStatus
	var err error
	if request.Method == http.MethodGet {
		query := request.URL.Query()
		if err = validateSingleQueryValues(query, "workspace_id", "run_id", "expected_descriptor_fingerprint", "target", "credential_ref"); err == nil {
			for _, values := range query {
				if len(values) != 1 || values[0] == "" {
					err = apperror.New(apperror.CodeInvalidArgument, "MCP credential binding query fields must each appear once with a value")
					break
				}
			}
		}
		if err == nil {
			value, err = controller.MCPCredentialStatus(request.Context(), application.MCPCredentialBinding{
				ServerID: serverID, WorkspaceID: query.Get("workspace_id"), RunID: query.Get("run_id"),
				ExpectedDescriptorFingerprint: query.Get("expected_descriptor_fingerprint"), Target: query.Get("target"), CredentialRef: query.Get("credential_ref")})
		}
	} else {
		body, bodyErr := readExtensionOnboardingBody(request, MaxControlRequestBodyBytes+4096)
		if bodyErr != nil {
			a.writeError(writer, requestID, bodyErr, runOperationErrorStatus(bodyErr))
			return
		}
		var view MCPCredentialRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Binding.ServerID != serverID {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "MCP credential path and binding do not match"), 0)
			return
		}
		value, err = controller.ChangeMCPCredential(request.Context(), application.ChangeMCPCredentialRequest{
			Binding: application.MCPCredentialBinding(view.Binding), Version: view.Version, Action: view.Action,
			Secret: view.Secret, OAuthFlowID: view.OAuthFlowID, OAuthIssuer: view.OAuthIssuer, OAuthClientID: view.OAuthClientID, OAuthClientIDMode: view.OAuthClientIDMode, OAuthClientSecret: view.OAuthClientSecret, OAuthCallbackPort: view.OAuthCallbackPort, Confirm: view.Confirm, ExpectedReferenceFingerprint: view.ExpectedReferenceFingerprint})
		view.Secret = ""
		view.OAuthClientSecret = ""
	}
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccess(writer, requestID, MCPCredentialStatusView(value), nil)
}
