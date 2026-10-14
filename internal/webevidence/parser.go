package webevidence

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

type ParsedDocument struct {
	Title       string
	Byline      string
	PublishedAt string
	Body        string
	MIME        string
	Charset     string
	Partial     bool
	Truncated   bool
}

func ParseDocument(raw []byte, contentType string, maxBodyBytes int) (ParsedDocument, error) {
	if maxBodyBytes <= 0 || maxBodyBytes > MaxBodyBytes {
		return ParsedDocument{}, errors.New("web parser body limit is invalid")
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ParsedDocument{}, errors.New("web response Content-Type is invalid")
	}
	mediaType = strings.ToLower(mediaType)
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		return parseHTML(raw, mediaType, params["charset"], maxBodyBytes)
	case strings.HasPrefix(mediaType, "text/") || mediaType == "application/json":
		return parseText(raw, mediaType, params["charset"], maxBodyBytes)
	case mediaType == "application/pdf":
		return parsePDF(raw, maxBodyBytes)
	default:
		return ParsedDocument{}, fmt.Errorf("web response MIME %q is not supported", mediaType)
	}
}

func parseHTML(raw []byte, mediaType, declaredCharset string,
	maxBodyBytes int,
) (ParsedDocument, error) {
	encoding, encodingName, _ := charset.DetermineEncoding(raw, mediaType+
		charsetParameter(declaredCharset))
	reader := encoding.NewDecoder().Reader(bytes.NewReader(raw))
	root, err := html.Parse(io.LimitReader(reader, int64(DefaultMaxResponse)+1))
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("parse bounded web HTML: %w", err)
	}
	result := ParsedDocument{MIME: mediaType, Charset: encodingName}
	var body strings.Builder
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, skipped bool) {
		if node.Type == html.ElementNode {
			name := strings.ToLower(node.Data)
			switch name {
			case "script", "style", "noscript", "svg", "canvas", "template", "form",
				"input", "button", "textarea", "select", "option", "iframe", "object",
				"embed", "audio", "video":
				skipped = true
			}
			if !skipped {
				switch name {
				case "title":
					result.Title = firstNonEmpty(result.Title, nodeText(node))
				case "meta":
					readHTMLMeta(node, &result)
				}
				if isBlockElement(name) && body.Len() > 0 {
					body.WriteByte('\n')
				}
			}
		}
		if node.Type == html.TextNode && !skipped {
			value := collapseSpace(node.Data)
			if value != "" {
				if body.Len() > 0 && !endsWithSpace(&body) {
					body.WriteByte(' ')
				}
				body.WriteString(value)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, skipped)
		}
	}
	walk(root, false)
	result.Title = boundedCleanText(result.Title, 1024)
	result.Byline = boundedCleanText(result.Byline, 512)
	result.PublishedAt = boundedCleanText(result.PublishedAt, 128)
	result.Body, result.Truncated = boundUTF8(
		normalizeLines(stripUnsafeControls(body.String())), maxBodyBytes)
	return result, nil
}

func parseText(raw []byte, mediaType, declaredCharset string,
	maxBodyBytes int,
) (ParsedDocument, error) {
	// JSON is UTF-8; other text defaults to UTF-8 unless its media type
	// explicitly declares an encoding. HTML sniffing does not apply here.
	var reader io.Reader = bytes.NewReader(raw)
	encodingName := "utf-8"
	if mediaType != "application/json" && strings.TrimSpace(declaredCharset) != "" {
		encoding, name := charset.Lookup(declaredCharset)
		if encoding == nil {
			return ParsedDocument{}, fmt.Errorf("unsupported web text charset %q", declaredCharset)
		}
		reader = encoding.NewDecoder().Reader(reader)
		encodingName = name
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, int64(DefaultMaxResponse)+1))
	if err != nil {
		return ParsedDocument{}, fmt.Errorf("read bounded web text: %w", err)
	}
	value := strings.ToValidUTF8(string(decoded), "�")
	value = stripUnsafeControls(value)
	body, truncated := boundUTF8(normalizeLines(value), maxBodyBytes)
	return ParsedDocument{Body: body, MIME: mediaType, Charset: encodingName,
		Truncated: truncated}, nil
}

var (
	pdfInfoPattern = regexp.MustCompile(`/((?:Title)|(?:Author)|(?:CreationDate))\s*\(([^)]{0,1000})\)`)
	pdfTextPattern = regexp.MustCompile(`\(([^)]{1,1000})\)\s*Tj|\[((?:[^\]]|\][^T]){1,1000})\]\s*TJ`)
	pdfArrayText   = regexp.MustCompile(`\(([^)]{1,1000})\)`)
)

// parsePDF is deliberately conservative: it never executes filters, embedded
// files, JavaScript, actions, forms, or external references. It extracts only
// bounded literal text operators and marks every result partial because full
// PDF layout/encoding interpretation is outside this first-party parser.
func parsePDF(raw []byte, maxBodyBytes int) (ParsedDocument, error) {
	if !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return ParsedDocument{}, errors.New("PDF response is missing the PDF signature")
	}
	result := ParsedDocument{MIME: "application/pdf", Charset: "binary",
		Partial: true}
	for _, match := range pdfInfoPattern.FindAllSubmatch(raw, 8) {
		value := boundedCleanText(unescapePDFLiteral(string(match[2])), 1024)
		switch string(match[1]) {
		case "Title":
			result.Title = firstNonEmpty(result.Title, value)
		case "Author":
			result.Byline = firstNonEmpty(result.Byline, value)
		case "CreationDate":
			result.PublishedAt = firstNonEmpty(result.PublishedAt, value)
		}
	}
	var body strings.Builder
	for _, match := range pdfTextPattern.FindAllSubmatch(raw, 4096) {
		var values [][]byte
		if len(match[1]) > 0 {
			values = [][]byte{match[1]}
		} else {
			for _, nested := range pdfArrayText.FindAllSubmatch(match[2], 256) {
				values = append(values, nested[1])
			}
		}
		for _, value := range values {
			clean := collapseSpace(stripUnsafeControls(unescapePDFLiteral(string(value))))
			if clean == "" {
				continue
			}
			if body.Len() > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(clean)
			if body.Len() > maxBodyBytes {
				break
			}
		}
		if body.Len() > maxBodyBytes {
			break
		}
	}
	if body.Len() == 0 {
		body.WriteString("[PDF document fetched; no safe literal text was extractable]")
	}
	result.Body, result.Truncated = boundUTF8(body.String(), maxBodyBytes)
	return result, nil
}

func readHTMLMeta(node *html.Node, result *ParsedDocument) {
	attributes := make(map[string]string, len(node.Attr))
	for _, attribute := range node.Attr {
		attributes[strings.ToLower(attribute.Key)] = strings.TrimSpace(attribute.Val)
	}
	key := strings.ToLower(firstNonEmpty(attributes["name"], attributes["property"],
		attributes["itemprop"]))
	value := attributes["content"]
	switch key {
	case "og:title", "twitter:title":
		result.Title = firstNonEmpty(result.Title, value)
	case "author", "article:author", "byl":
		result.Byline = firstNonEmpty(result.Byline, value)
	case "article:published_time", "date", "datepublished", "pubdate":
		result.PublishedAt = firstNonEmpty(result.PublishedAt, value)
	}
}

func nodeText(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return collapseSpace(builder.String())
}

func isBlockElement(name string) bool {
	switch name {
	case "article", "aside", "blockquote", "br", "dd", "div", "dl", "dt", "figcaption",
		"figure", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr",
		"li", "main", "nav", "ol", "p", "pre", "section", "table", "tbody", "td",
		"tfoot", "th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

func endsWithSpace(builder *strings.Builder) bool {
	value := builder.String()
	return value != "" && unicode.IsSpace(rune(value[len(value)-1]))
}

func collapseSpace(value string) string { return strings.Join(strings.Fields(value), " ") }

func normalizeLines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	lastBlank := true
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if !lastBlank {
				out = append(out, "")
			}
			lastBlank = true
			continue
		}
		out = append(out, line)
		lastBlank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripUnsafeControls(value string) string {
	return strings.Map(func(current rune) rune {
		if current == '\n' || current == '\t' {
			return current
		}
		if unicode.IsControl(current) {
			return -1
		}
		return current
	}, value)
}

func boundedCleanText(value string, maxRunes int) string {
	value = collapseSpace(stripUnsafeControls(strings.ToValidUTF8(value, "�")))
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}

func boundedSnippet(value string) string {
	value = boundedCleanText(value, 2048)
	value, _ = boundUTF8(value, MaxSnippetBytes)
	return value
}

func boundUTF8(value string, maxBytes int) (string, bool) {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxBytes {
		return value, false
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func normalizedCharset(value string) string {
	return strings.ToLower(boundedCleanText(value, 128))
}

func charsetParameter(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "; charset=" + value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func unescapePDFLiteral(value string) string {
	replacer := strings.NewReplacer(`\n`, "\n", `\r`, "\n", `\t`, "\t", `\(`, "(",
		`\)`, ")", `\\`, `\`)
	return strings.ToValidUTF8(replacer.Replace(value), "�")
}
