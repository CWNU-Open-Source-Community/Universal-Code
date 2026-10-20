package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"cyberagent-workbench/internal/webevidence"
)

type linkHTTPFixture struct{ calls []string }

func (f *linkHTTPFixture) Get(_ context.Context, url string, _ int, _ string) (webevidence.HTTPDocument, error) {
	f.calls = append(f.calls, url)
	return webevidence.HTTPDocument{FinalURL: url, StatusCode: 200, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body: []byte(`<html><head><title>Fallback</title><meta property="og:title" content="PR #405 &amp; review"><meta name="description" content="A file review"></head><body><script>alert(1)</script></body></html>`)}, nil
}

func TestChatLinkPreviewLive(t *testing.T) {
	output := os.Getenv("UC_PREVIEW_EVIDENCE")
	if output == "" {
		t.Skip("requires an explicit public website preview verification")
	}
	target := os.Getenv("UC_PREVIEW_URL")
	if target == "" {
		target = "https://signpath.org/"
	}
	view, err := loadLinkPreview(t.Context(), webevidence.NewSafeHTTPClient(), target)
	if err != nil {
		t.Fatal(err)
	}
	if view.Title == "" {
		t.Fatal("live page returned no title")
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLinkPreviewReadsBoundedMetadataAndRejectsPrivateTargets(t *testing.T) {
	client := &linkHTTPFixture{}
	view, err := loadLinkPreview(context.Background(), client, "https://example.com/review")
	if err != nil || view.Title != "PR #405 & review" || view.Description != "A file review" || view.Image != "" || len(client.calls) != 1 {
		t.Fatalf("preview: %#v %v", view, err)
	}
	for _, target := range []string{"https://127.0.0.1/", "https://localhost/", "https://169.254.169.254/", "file:///secret", "https://user:password@example.com/"} {
		if _, err := loadLinkPreview(context.Background(), client, target); err == nil {
			t.Fatalf("private target accepted: %s", target)
		}
	}
	if len(client.calls) != 1 {
		t.Fatal("invalid URLs reached transport")
	}
}

func TestLinkPreviewMetadataIsTextOnlyAndBounded(t *testing.T) {
	view, image := parseLinkPreview([]byte(`<title>Plain &amp; readable</title><meta property="og:description" content="&lt;script&gt;not executed&lt;/script&gt;"><meta property="og:image" content="/preview.png">`), "https://example.com/")
	if view.Title != "Plain & readable" || view.Description != "<script>not executed</script>" || image != "/preview.png" {
		t.Fatalf("unexpected metadata: %#v %q", view, image)
	}
}
