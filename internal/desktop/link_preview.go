package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/webevidence"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
)

type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

type previewHTTPClient interface {
	Get(context.Context, string, int, string) (webevidence.HTTPDocument, error)
}

// This is an explicit reader hover/focus request, not an Agent network grant.
// SafeHTTPClient pins public DNS and rechecks redirects; no cookies, application
// bearer tokens, scripts or third-party preview services are involved.
func (b *DesktopBridge) PreviewExternalURL(raw string) (LinkPreview, error) {
	ctx, err := b.lifecycleContext()
	if err != nil {
		return LinkPreview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return loadLinkPreview(ctx, webevidence.NewSafeHTTPClient(), raw)
}

func loadLinkPreview(ctx context.Context, client previewHTTPClient, raw string) (LinkPreview, error) {
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(raw)
	if err != nil {
		return LinkPreview{}, err
	}
	doc, err := client.Get(ctx, canonical, 512*1024, "text/html,application/xhtml+xml")
	if err != nil {
		return LinkPreview{}, apperror.New(apperror.CodeUnavailable, "Page preview could not be loaded")
	}
	media, _, _ := mime.ParseMediaType(doc.Header.Get("Content-Type"))
	if doc.StatusCode < 200 || doc.StatusCode >= 300 || (media != "text/html" && media != "application/xhtml+xml") {
		return LinkPreview{}, apperror.New(apperror.CodeUnavailable, "This address has no web page preview")
	}
	preview, imageURL := parseLinkPreview(doc.Body, raw)
	base, _ := url.Parse(doc.FinalURL)
	if base != nil && imageURL != "" {
		ref, err := url.Parse(imageURL)
		if err == nil {
			if imageDoc, err := client.Get(ctx, base.ResolveReference(ref).String(), 512*1024, "image/png,image/jpeg,image/webp"); err == nil && !imageDoc.Truncated && imageDoc.StatusCode == 200 {
				config, format, err := image.DecodeConfig(bytes.NewReader(imageDoc.Body))
				if err == nil && config.Width > 0 && config.Height > 0 && int64(config.Width)*int64(config.Height) <= 16_000_000 &&
					(format == "png" || format == "jpeg" || format == "webp") {
					preview.Image = "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(imageDoc.Body)
				}
			}
		}
	}
	return preview, nil
}

func parseLinkPreview(body []byte, raw string) (LinkPreview, string) {
	result := LinkPreview{URL: raw}
	imageURL, fallbackTitle, fallbackDescription := "", "", ""
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	inTitle := false
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if result.Title == "" {
				result.Title = fallbackTitle
			}
			if result.Description == "" {
				result.Description = fallbackDescription
			}
			return result, imageURL
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.Data == "title" {
				inTitle = true
			}
			if token.Data != "meta" {
				continue
			}
			name, content := "", ""
			for _, attr := range token.Attr {
				if attr.Key == "property" || attr.Key == "name" {
					name = strings.ToLower(attr.Val)
				}
				if attr.Key == "content" {
					content = attr.Val
				}
			}
			switch name {
			case "og:title":
				result.Title = previewText(content, 240)
			case "og:description":
				result.Description = previewText(content, 500)
			case "description":
				fallbackDescription = previewText(content, 500)
			case "og:image":
				if len(content) <= 8192 {
					imageURL = content
				}
			}
		case html.TextToken:
			if inTitle {
				fallbackTitle = previewText(fallbackTitle+tokenizer.Token().Data, 240)
			}
		case html.EndTagToken:
			if tokenizer.Token().Data == "title" {
				inTitle = false
			}
		}
	}
}

func previewText(raw string, maximum int) string {
	runes := []rune(strings.Join(strings.FieldsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " "))
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}

func (b *DesktopBridge) SaveExternalURL(raw string) (string, error) {
	if b == nil || b.resourceHost == nil {
		return "", apperror.New(apperror.CodeUnavailable, "Save dialog is unavailable")
	}
	canonical, err := webevidence.CanonicalizePublicHTTPSURL(raw)
	if err != nil {
		return "", err
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return "", err
	}
	fetchContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	doc, err := webevidence.NewSafeHTTPClient().Get(fetchContext, canonical, 2*1024*1024, "*/*")
	cancel()
	if err != nil || doc.StatusCode != 200 || doc.Truncated {
		return "", apperror.New(apperror.CodeUnavailable, "Link could not be saved (maximum 2 MiB)")
	}
	u, _ := url.Parse(canonical)
	name := path.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		name = u.Hostname() + ".html"
	}
	return b.resourceHost.FileAction(ctx, "save_as", ChatResource{Name: name, MIMEType: doc.Header.Get("Content-Type"), Data: doc.Body})
}
