package webevidence

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestAtomTextConstructsKeepArticleContent(t *testing.T) {
	for _, test := range []struct {
		name, content, want string
		partial             bool
	}{
		{"text", `<content type="text">Article &lt;body&gt; sentinel</content>`, "Article <body> sentinel", false},
		{"html", `<content type="html">&lt;p&gt;Article &lt;b&gt;body&lt;/b&gt; sentinel&lt;/p&gt;</content>`, "Article body sentinel", false},
		{"xhtml", `<content type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><p>Article <b>body</b> sentinel</p></div></content>`, "Article body sentinel", false},
		{"summary", `<summary type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml"><p>Summary body</p></div></summary>`, "Summary body", false},
		{"external", `<content src="https://example.com/body" type="text/plain"/><summary>Available summary</summary>`, "Available summary", true},
		{"unsupported", `<content type="image/png">base64</content><summary>Available summary</summary>`, "Available summary", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			feed := `<feed xmlns="http://www.w3.org/2005/Atom"><title>Example</title><entry><id>https://example.com/entry</id><title>Entry</title>` + test.content + `</entry></feed>`
			requests := 0
			client := &SafeHTTPClient{
				Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
				}),
				TransportFactory: func(string, []netip.Addr) http.RoundTripper {
					return roundTripFunc(func(request *http.Request) (*http.Response, error) {
						requests++
						if request.URL.Path != "/feed" {
							t.Fatalf("unexpected implicit request: %s", request.URL)
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/atom+xml"}}, Body: io.NopCloser(strings.NewReader(feed))}, nil
					})
				},
			}
			document, err := NewRSSSourceConnector(client).Read(t.Context(), "https://example.com/feed", 10,
				NetworkAuthority{Mode: "allowlist", AllowedTargets: []string{"example.com"}})
			if err != nil || !strings.Contains(document.Body, test.want) {
				t.Fatalf("document=%+v err=%v", document, err)
			}
			if document.Truncated != test.partial || document.ItemsIncluded != 1 || document.ItemsAvailable != 1 || requests != 1 {
				t.Fatalf("coverage=%+v requests=%d", document, requests)
			}
			if test.partial && document.TruncationReason != "entry_content_unavailable" {
				t.Fatalf("missing content is not reported: %+v", document)
			}
		})
	}
}
