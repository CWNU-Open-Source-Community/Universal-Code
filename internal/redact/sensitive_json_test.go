package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSensitiveJSONFieldNameDistinguishesCredentialsFromReferences(t *testing.T) {
	for _, value := range []string{"password", "authentication", "auth_header",
		"accessKey", "signing_key", "credentials", "X-API-Key"} {
		if !SensitiveJSONFieldName(value) {
			t.Fatalf("sensitive field %q was not recognized", value)
		}
	}
	for _, value := range []string{"credential_ref", "token_id", "secret_name",
		"api_key_reference", "token_count", "max_tokens", "authority"} {
		if SensitiveJSONFieldName(value) {
			t.Fatalf("reference or metadata field %q was classified as a credential", value)
		}
	}
}

func TestSanitizeSensitiveJSONRedactsFieldsBeforeAnyTruncation(t *testing.T) {
	password := "short phrase"
	raw, err := json.Marshal(map[string]any{
		"password": password,
		"nested":   []any{map[string]any{"auth_header": "ordinary-value"}},
		"safe":     "visible",
	})
	if err != nil {
		t.Fatal(err)
	}
	safe, err := SanitizeSensitiveJSON(raw)
	if err != nil || strings.Contains(string(safe), password) ||
		strings.Contains(string(safe), "ordinary-value") ||
		!strings.Contains(string(safe), SensitiveJSONFieldPlaceholder) ||
		!strings.Contains(string(safe), `"safe":"visible"`) {
		t.Fatalf("sensitive JSON projection=%s err=%v", safe, err)
	}
}

// The PASS family (db_pass, app_pass) is as credential-shaped as password:
// the #660 regex-layer extension has a matching field-name counterpart.
func TestSensitiveJSONFieldNameCoversPassFamily(t *testing.T) {
	for _, value := range []string{"db_pass", "app_pass", "pass", "login_pass"} {
		if !SensitiveJSONFieldName(value) {
			t.Fatalf("credential field %q was not recognized", value)
		}
	}
	for _, value := range []string{"passthrough", "pass_through", "pass_reference", "bypass"} {
		if SensitiveJSONFieldName(value) {
			t.Fatalf("non-credential field %q was classified as a credential", value)
		}
	}
}

func TestSanitizePreservesBooleanPassResults(t *testing.T) {
	raw := []byte(`{"verification_pass":false,"pass":true,"db_pass":"fixture","pass_through":"visible"}`)
	safe, err := SanitizeSensitiveJSON(raw)
	want := `{"db_pass":"[REDACTED:sensitive-field]","pass":true,"pass_through":"visible","verification_pass":false}`
	if err != nil || string(safe) != want {
		t.Fatalf("sanitized pass fields = %s, err = %v", safe, err)
	}
}
