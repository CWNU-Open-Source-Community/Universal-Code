package webevidence

import (
	"encoding/base64"
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CanonicalizeGoProxyArchiveRedirect accepts only the public Go proxy's signed
// archive handoff. Callers still authorize the storage origin and pin its DNS;
// the broker verifies the downloaded archive against the reviewed go.sum.
// The signed path and query retain their original encoding.
func CanonicalizeGoProxyArchiveRedirect(source, raw string) (string, error) {
	denied := errors.New("signed artifact redirect is outside the Go proxy archive policy")
	canonicalSource, err := CanonicalizePublicHTTPSURL(source)
	if err != nil {
		return "", denied
	}
	from, err := url.Parse(canonicalSource)
	if err != nil || from.Host != "proxy.golang.org" || from.RawQuery != "" {
		return "", denied
	}
	modulePath, versionFile, found := strings.Cut(from.Path, "/@v/")
	if !found || modulePath == "" || versionFile == ".zip" || !strings.HasSuffix(versionFile, ".zip") || strings.Contains(versionFile, "/") {
		return "", denied
	}
	if len(raw) > 4096 || !utf8.ValidString(raw) || containsControl(raw) || strings.Contains(raw, `\`) {
		return "", denied
	}
	destination, err := url.Parse(raw)
	if err != nil || destination.Fragment != "" || destination.User != nil || destination.Opaque != "" {
		return "", denied
	}
	unsigned := *destination
	unsigned.RawQuery = ""
	canonicalDestination, err := CanonicalizePublicHTTPSURL(unsigned.String())
	if err != nil {
		return "", denied
	}
	checked, err := url.Parse(canonicalDestination)
	if err != nil || checked.Host != "storage.googleapis.com" || checked.Path != destination.Path ||
		!strings.HasPrefix(checked.Path, "/proxy-golang-org-prod/") || !strings.HasSuffix(checked.Path, ".zip") ||
		path.Base(checked.Path) != strings.TrimPrefix(checked.Path, "/proxy-golang-org-prod/") {
		return "", denied
	}
	query, err := url.ParseQuery(destination.RawQuery)
	if err != nil || len(query) != 3 {
		return "", denied
	}
	for _, key := range []string{"Expires", "GoogleAccessId", "Signature"} {
		values := query[key]
		if len(values) != 1 || values[0] == "" || containsControl(values[0]) {
			return "", denied
		}
	}
	expires, err := strconv.ParseInt(query.Get("Expires"), 10, 64)
	if err != nil || expires <= 0 {
		return "", denied
	}
	signature, err := base64.StdEncoding.DecodeString(query.Get("Signature"))
	if err != nil || len(signature) == 0 {
		return "", denied
	}
	destination.Scheme, destination.Host = checked.Scheme, checked.Host
	return destination.String(), nil
}
