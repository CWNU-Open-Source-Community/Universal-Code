package webevidence

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"mime"
	"strings"
)

const rssConnectorVersion = "rss-atom-public.v1"

type RSSSourceConnector struct{ client *SafeHTTPClient }

func NewRSSSourceConnector(client *SafeHTTPClient) *RSSSourceConnector {
	if client == nil {
		client = NewSafeHTTPClient()
	}
	return &RSSSourceConnector{client: client}
}

func (*RSSSourceConnector) Name() string           { return "rss" }
func (*RSSSourceConnector) Version() string        { return rssConnectorVersion }
func (*RSSSourceConnector) SearchEndpoint() string { return "" }
func (*RSSSourceConnector) MatchURL(string) bool   { return false }
func (*RSSSourceConnector) Search(context.Context, string, int,
	NetworkAuthority,
) ([]ConnectorSearchItem, error) {
	return nil, newConnectorError("search_unsupported", nil)
}

type rssEnvelope struct {
	XMLName xml.Name
	Channel struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		Items       []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			GUID        string `xml:"guid"`
			Author      string `xml:"author"`
			Creator     string `xml:"creator"`
			PubDate     string `xml:"pubDate"`
			Description string `xml:"description"`
			Content     string `xml:"encoded"`
		} `xml:"item"`
	} `xml:"channel"`
	Title    string `xml:"title"`
	Subtitle string `xml:"subtitle"`
	Entries  []struct {
		Title     string            `xml:"title"`
		ID        string            `xml:"id"`
		Published string            `xml:"published"`
		Updated   string            `xml:"updated"`
		Summary   atomTextConstruct `xml:"summary"`
		Content   atomTextConstruct `xml:"content"`
		Author    struct {
			Name string `xml:"name"`
		} `xml:"author"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

// Atom text constructs encode plain text, escaped HTML, or nested XHTML.
// External content is not fetched implicitly by the feed reader.
type atomTextConstruct struct {
	Type  string `xml:"type,attr"`
	Src   string `xml:"src,attr"`
	Text  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

func (value atomTextConstruct) body() (string, bool) {
	if value.Src != "" {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(value.Type)) {
	case "", "text", "text/plain":
		return boundedConnectorText(value.Text, MaxBodyBytes), true
	case "html", "text/html":
		return connectorHTMLText(value.Text), true
	case "xhtml", "application/xhtml+xml":
		return connectorHTMLText(value.Inner), true
	default:
		return "", false
	}
}

func (c *RSSSourceConnector) Read(ctx context.Context, rawURL string, maxItems int,
	authority NetworkAuthority,
) (ConnectorDocument, error) {
	if maxItems <= 0 {
		maxItems = DefaultConnectorItemLimit
	}
	if maxItems > MaxConnectorItemLimit {
		return ConnectorDocument{}, newConnectorError("invalid_request", nil)
	}
	canonical, err := CanonicalizePublicHTTPSURL(rawURL)
	if err != nil {
		return ConnectorDocument{}, newConnectorError("unsupported_url", err)
	}
	document, err := connectorGet(ctx, c.client, canonical,
		"application/rss+xml,application/atom+xml,application/xml,text/xml;q=0.9", authority)
	if err != nil {
		return ConnectorDocument{}, err
	}
	mediaType, _, mediaErr := mime.ParseMediaType(document.Header.Get("Content-Type"))
	if mediaErr != nil || (mediaType != "application/rss+xml" &&
		mediaType != "application/atom+xml" && mediaType != "application/xml" &&
		mediaType != "text/xml") {
		return ConnectorDocument{}, newConnectorError("unsupported_content",
			errors.New("feed response MIME is not RSS or Atom XML"))
	}
	var feed rssEnvelope
	if err := xml.Unmarshal(document.Body, &feed); err != nil {
		return ConnectorDocument{}, newConnectorError("invalid_response", err)
	}
	isRSS := len(feed.Channel.Items) > 0 || strings.TrimSpace(feed.Channel.Title) != ""
	isAtom := len(feed.Entries) > 0 || strings.EqualFold(feed.XMLName.Local, "feed")
	if !isRSS && !isAtom {
		return ConnectorDocument{}, newConnectorError("invalid_response",
			errors.New("XML document is not a recognized RSS or Atom feed"))
	}
	var builder strings.Builder
	feedTitle := boundedConnectorText(feed.Channel.Title, 4096)
	feedDescription := connectorHTMLText(feed.Channel.Description)
	total := len(feed.Channel.Items)
	contentKind := "rss_feed"
	if isAtom && !isRSS {
		feedTitle = boundedConnectorText(feed.Title, 4096)
		feedDescription = connectorHTMLText(feed.Subtitle)
		total = len(feed.Entries)
		contentKind = "atom_feed"
	}
	if feedTitle == "" {
		feedTitle = "RSS/Atom feed"
	}
	markdownSection(&builder, "# "+feedTitle,
		fmt.Sprintf("Feed URL: %s", canonical))
	markdownSection(&builder, "## Description", feedDescription)
	included := min(total, maxItems)
	missingContent := false
	for index := 0; index < included; index++ {
		var title, link, author, published, body string
		if contentKind == "rss_feed" {
			item := feed.Channel.Items[index]
			title, link = item.Title, firstNonEmpty(item.Link, item.GUID)
			author, published = firstNonEmpty(item.Creator, item.Author), item.PubDate
			body = connectorHTMLText(firstNonEmpty(item.Content, item.Description))
		} else {
			entry := feed.Entries[index]
			title, author = entry.Title, entry.Author.Name
			published = firstNonEmpty(entry.Published, entry.Updated)
			var complete bool
			body, complete = entry.Content.body()
			missingContent = missingContent || !complete
			if body == "" {
				body, complete = entry.Summary.body()
				missingContent = missingContent || !complete
			}
			for _, candidate := range entry.Links {
				if candidate.Rel == "" || candidate.Rel == "alternate" {
					link = candidate.Href
					break
				}
			}
			if link == "" {
				link = entry.ID
			}
		}
		entryMetadata := ""
		if candidate, canonicalErr := CanonicalizePublicHTTPSURL(link); canonicalErr == nil {
			entryMetadata += "URL: " + candidate
		}
		if strings.TrimSpace(author) != "" {
			entryMetadata += "\nAuthor: " + boundedConnectorText(author, 512)
		}
		if strings.TrimSpace(published) != "" {
			entryMetadata += "\nPublished: " + boundedConnectorText(published, 128)
		}
		entryBody := strings.TrimSpace(entryMetadata)
		if body != "" {
			if entryBody != "" {
				entryBody += "\n\n"
			}
			entryBody += body
		}
		markdownSection(&builder, fmt.Sprintf("## Entry %d: %s", index+1,
			boundedConnectorText(title, 4096)), entryBody)
	}
	truncated := total > included || missingContent
	coverage, reason := "feed_entries", ""
	if missingContent {
		coverage, reason = "feed_entries_with_missing_content", "entry_content_unavailable"
	}
	if total > included {
		reason = "entry_limit"
	}
	return ConnectorDocument{CanonicalURL: canonical,
		RequestEndpoints: []string{document.RequestedURL}, HTTPStatus: document.StatusCode,
		RawDigest: DigestBytes(document.Body), Title: boundedCleanText(feedTitle, 1024),
		MIME: "text/markdown", Charset: "utf-8", Body: builder.String(),
		ContentKind: contentKind, Coverage: coverage,
		ItemsIncluded: included, ItemsAvailable: total, Truncated: truncated,
		TruncationReason: reason}, nil
}
