package llm

import "testing"

func TestModelCatalogSuppressesCredentialEcho(t *testing.T) {
	const secret = "short-ordinary-key"
	models, _, _, err := parseModelCatalogPage([]byte(`{"data":[{"id":"short-ordinary-key"},{"id":"valid-model","display_name":"contains short-ordinary-key"}]}`), false, secret)
	if err != nil || len(models) != 1 || models[0].ID != "valid-model" || models[0].DisplayName != "" {
		t.Fatalf("key echo not suppressed: %#v %v", models, err)
	}
}

func TestModelCatalogGoogleCapacitiesStaySeparate(t *testing.T) {
	models, next, more, err := parseModelCatalogPage([]byte(`{"models":[{"name":"models/gemini-test","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent"]},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"opaque-cursor"}`), true, "")
	if err != nil || len(models) != 1 || models[0].ID != "gemini-test" || models[0].InputTokenLimit != 1048576 || models[0].OutputTokenLimit != 65536 || next != "opaque-cursor" || !more {
		t.Fatalf("Google model info: %#v %q %v %v", models, next, more, err)
	}
}
