package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	Version             = "web-ui.v1"
	MaxIndexBytes       = 1024 * 1024
	MaxAssetBytes       = 8 * 1024 * 1024
	MaxTTFAssetBytes    = 9 * 1024 * 1024
	MaxBundleBytes      = 64 * 1024 * 1024
	MaxAssetCount       = 256
	MaxFallbackSegments = 8
)

var allowedAssetTypes = map[string]string{
	".avif":  "image/avif",
	".css":   "text/css; charset=utf-8",
	".gif":   "image/gif",
	".ico":   "image/x-icon",
	".jpeg":  "image/jpeg",
	".jpg":   "image/jpeg",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".ttf":   "font/ttf",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

var allowedRootAssets = []struct {
	name        string
	contentType string
}{
	{name: "apple-touch-icon.png", contentType: "image/png"},
	{name: "traverse-board-favicon-32.png", contentType: "image/png"},
	{name: "THIRD-PARTY-NOTICES.txt", contentType: "text/plain; charset=utf-8"},
	{name: "licenses/HarmonyOS-Sans.txt", contentType: "text/plain; charset=utf-8"},
}

type asset struct {
	body        []byte
	contentType string
	etag        string
	immutable   bool
}

type Bundle struct {
	source string
	digest string
	assets map[string]asset
	index  asset
}

func LoadDirectory(directory string) (*Bundle, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, errors.New("web UI directory is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(directory))
	if err != nil {
		return nil, fmt.Errorf("resolve Web UI directory: %w", err)
	}
	rootInfo, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect Web UI directory: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("web UI path must be a real directory, not a symbolic link")
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("open Web UI directory: %w", err)
	}
	defer root.Close()

	indexBody, err := readRegularFile(root, "index.html", MaxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("load Web UI index: %w", err)
	}
	if !utf8.Valid(indexBody) {
		return nil, errors.New("web UI index must be valid UTF-8")
	}
	assetsInfo, err := root.Lstat("assets")
	if err != nil {
		return nil, fmt.Errorf("inspect Web UI assets: %w", err)
	}
	if assetsInfo.Mode()&os.ModeSymlink != 0 || !assetsInfo.IsDir() {
		return nil, errors.New("web UI assets path must be a real directory")
	}

	bundle := &Bundle{
		source: absolute,
		assets: make(map[string]asset),
		index:  makeAsset(indexBody, "text/html; charset=utf-8", false),
	}
	totalBytes := int64(len(indexBody))
	for _, spec := range allowedRootAssets {
		body, err := readRegularFile(root, spec.name, MaxAssetBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load Web UI root asset %q: %w", spec.name, err)
		}
		totalBytes += int64(len(body))
		if totalBytes > MaxBundleBytes {
			return nil, fmt.Errorf("web UI bundle exceeds %d bytes", MaxBundleBytes)
		}
		bundle.assets["/"+spec.name] = makeAsset(body, spec.contentType, false)
	}
	hashedAssetCount := 0
	err = fs.WalkDir(root.FS(), "assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "assets" {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("web UI bundle contains symbolic link %q", name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("web UI bundle contains non-regular asset %q", name)
		}
		if len(bundle.assets) >= MaxAssetCount {
			return fmt.Errorf("web UI bundle exceeds %d assets", MaxAssetCount)
		}
		extension := strings.ToLower(filepath.Ext(name))
		contentType, allowed := allowedAssetTypes[extension]
		if !allowed {
			return fmt.Errorf("web UI asset %q has unsupported extension %q", name, extension)
		}
		if !assetNameHasDigest(filepath.Base(name), extension) {
			return fmt.Errorf("web UI asset %q does not have a content-hashed name", name)
		}
		body, err := readRegularFile(root, filepath.ToSlash(name), maxAssetBytes(extension))
		if err != nil {
			return fmt.Errorf("load Web UI asset %q: %w", name, err)
		}
		totalBytes += int64(len(body))
		if totalBytes > MaxBundleBytes {
			return fmt.Errorf("web UI bundle exceeds %d bytes", MaxBundleBytes)
		}
		urlPath := "/" + path.Clean(filepath.ToSlash(name))
		bundle.assets[urlPath] = makeAsset(body, contentType, true)
		hashedAssetCount++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hashedAssetCount == 0 {
		return nil, errors.New("web UI bundle must contain at least one hashed asset")
	}
	bundle.digest = bundleDigest(bundle.index, bundle.assets)
	return bundle, nil
}

// LoadEmbeddedFS snapshots a compile-time embedded production bundle. It is
// intentionally separate from LoadDirectory so the Desktop shell never falls
// back to mutable HTML or JavaScript beside the executable.
func LoadEmbeddedFS(source fs.FS, root string) (*Bundle, error) {
	root = strings.TrimSpace(root)
	if source == nil || root == "" || root == "." || !fs.ValidPath(root) {
		return nil, errors.New("embedded Web UI root is invalid")
	}
	rootInfo, err := fs.Stat(source, root)
	if err != nil {
		return nil, fmt.Errorf("inspect embedded Web UI root: %w", err)
	}
	if rootInfo.Mode()&fs.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("embedded Web UI root must be a real directory")
	}
	sub, err := fs.Sub(source, root)
	if err != nil {
		return nil, fmt.Errorf("open embedded Web UI root: %w", err)
	}
	indexBody, err := readEmbeddedRegularFile(sub, "index.html", MaxIndexBytes)
	if err != nil {
		return nil, fmt.Errorf("load embedded Web UI index: %w", err)
	}
	if !utf8.Valid(indexBody) {
		return nil, errors.New("embedded Web UI index must be valid UTF-8")
	}
	assetsInfo, err := fs.Stat(sub, "assets")
	if err != nil {
		return nil, fmt.Errorf("inspect embedded Web UI assets: %w", err)
	}
	if assetsInfo.Mode()&fs.ModeSymlink != 0 || !assetsInfo.IsDir() {
		return nil, errors.New("embedded Web UI assets must be a real directory")
	}

	bundle := &Bundle{
		source: "embedded:" + root,
		assets: make(map[string]asset),
		index:  makeAsset(indexBody, "text/html; charset=utf-8", false),
	}
	totalBytes := int64(len(indexBody))
	for _, spec := range allowedRootAssets {
		body, err := readEmbeddedRegularFile(sub, spec.name, MaxAssetBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load embedded Web UI root asset %q: %w", spec.name, err)
		}
		totalBytes += int64(len(body))
		if totalBytes > MaxBundleBytes {
			return nil, fmt.Errorf("embedded Web UI bundle exceeds %d bytes", MaxBundleBytes)
		}
		bundle.assets["/"+spec.name] = makeAsset(body, spec.contentType, false)
	}
	hashedAssetCount := 0
	err = fs.WalkDir(sub, "assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "assets" {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("embedded Web UI bundle contains symbolic link %q", name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("embedded Web UI bundle contains non-regular asset %q", name)
		}
		if len(bundle.assets) >= MaxAssetCount {
			return fmt.Errorf("embedded Web UI bundle exceeds %d assets", MaxAssetCount)
		}
		extension := strings.ToLower(path.Ext(name))
		contentType, allowed := allowedAssetTypes[extension]
		if !allowed {
			return fmt.Errorf("embedded Web UI asset %q has unsupported extension %q", name, extension)
		}
		if !assetNameHasDigest(path.Base(name), extension) {
			return fmt.Errorf("embedded Web UI asset %q does not have a content-hashed name", name)
		}
		body, err := readEmbeddedRegularFile(sub, name, maxAssetBytes(extension))
		if err != nil {
			return fmt.Errorf("load embedded Web UI asset %q: %w", name, err)
		}
		totalBytes += int64(len(body))
		if totalBytes > MaxBundleBytes {
			return fmt.Errorf("embedded Web UI bundle exceeds %d bytes", MaxBundleBytes)
		}
		urlPath := "/" + path.Clean(name)
		bundle.assets[urlPath] = makeAsset(body, contentType, true)
		hashedAssetCount++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if hashedAssetCount == 0 {
		return nil, errors.New("embedded Web UI bundle must contain at least one hashed asset")
	}
	bundle.digest = bundleDigest(bundle.index, bundle.assets)
	return bundle, nil
}

func (b *Bundle) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	b.serveHTTP(writer, request, "")
}

// ServeHTTPWithStyleNonce renders the index with the request's CSP style nonce.
// Hashed assets and the frozen bundle remain unchanged.
func (b *Bundle) ServeHTTPWithStyleNonce(writer http.ResponseWriter, request *http.Request, nonce string) {
	b.serveHTTP(writer, request, nonce)
}

func (b *Bundle) serveHTTP(writer http.ResponseWriter, request *http.Request, styleNonce string) {
	if b == nil || request == nil || request.URL == nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path == "/" {
		serveIndex(writer, request, b.index, styleNonce)
		return
	}
	if current, ok := b.assets[request.URL.Path]; ok {
		serveAsset(writer, request, current)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/assets/") || path.Ext(request.URL.Path) != "" ||
		!acceptsHTML(request.Header.Values("Accept")) || !boundedFallbackPath(request.URL.Path) {
		http.NotFound(writer, request)
		return
	}
	serveIndex(writer, request, b.index, styleNonce)
}

func (b *Bundle) Source() string {
	if b == nil {
		return ""
	}
	return b.source
}

func (b *Bundle) Digest() string {
	if b == nil {
		return ""
	}
	return b.digest
}

func (b *Bundle) AssetCount() int {
	if b == nil {
		return 0
	}
	return len(b.assets)
}

func maxAssetBytes(extension string) int64 {
	if extension == ".ttf" {
		return MaxTTFAssetBytes
	}
	return MaxAssetBytes
}

func readRegularFile(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("file must be regular and cannot be a symbolic link")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() < 0 || openedInfo.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes or is not regular", limit)
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func readEmbeddedRegularFile(source fs.FS, name string, limit int64) ([]byte, error) {
	if !fs.ValidPath(name) || limit < 0 {
		return nil, errors.New("embedded file request is invalid")
	}
	file, err := source.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if before.Mode()&fs.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Size() < 0 || before.Size() > limit {
		return nil, fmt.Errorf("embedded file exceeds %d bytes or is not regular", limit)
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit || int64(len(body)) != before.Size() ||
		!after.Mode().IsRegular() || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		return nil, errors.New("embedded file changed or exceeded its bound during read")
	}
	return body, nil
}

func assetNameHasDigest(name string, extension string) bool {
	base := strings.TrimSuffix(name, extension)
	for separator := len(base) - 1; separator >= 0; separator-- {
		if base[separator] != '-' && base[separator] != '.' {
			continue
		}
		digest := base[separator+1:]
		if len(digest) < 8 {
			continue
		}
		valid := true
		for _, current := range digest {
			if (current < 'a' || current > 'z') && (current < 'A' || current > 'Z') &&
				(current < '0' || current > '9') && current != '_' && current != '-' {
				valid = false
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func makeAsset(body []byte, contentType string, immutable bool) asset {
	digest := sha256.Sum256(body)
	return asset{
		body: append([]byte(nil), body...), contentType: contentType,
		etag: `"` + hex.EncodeToString(digest[:]) + `"`, immutable: immutable,
	}
}

func serveAsset(writer http.ResponseWriter, request *http.Request, current asset) {
	header := writer.Header()
	header.Set("Content-Type", current.contentType)
	header.Set("Content-Length", strconv.Itoa(len(current.body)))
	header.Set("ETag", current.etag)
	if current.immutable {
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		header.Set("Cache-Control", "no-store")
	}
	if request.Header.Get("If-None-Match") == current.etag {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(current.body)
	}
}

func serveIndex(writer http.ResponseWriter, request *http.Request, current asset, styleNonce string) {
	if styleNonce == "" {
		serveAsset(writer, request, current)
		return
	}
	body := bytes.ReplaceAll(current.body, []byte("__UC_STYLE_NONCE__"), []byte(html.EscapeString(styleNonce)))
	header := writer.Header()
	header.Set("Content-Type", current.contentType)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("Cache-Control", "no-store")
	header.Del("ETag")
	// A 304 could pair a fresh CSP nonce with an older document's meta element.
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = writer.Write(body)
	}
}

func acceptsHTML(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			if quality := parameters["q"]; quality != "" {
				parsed, err := strconv.ParseFloat(quality, 64)
				if err != nil || parsed <= 0 {
					continue
				}
			}
			if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
				continue
			}
			return true
		}
	}
	return false
}

func boundedFallbackPath(value string) bool {
	if value == "" || value[0] != '/' || value != path.Clean(value) || strings.Contains(value, `\`) {
		return false
	}
	segments := 0
	for _, current := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if current == "" {
			continue
		}
		segments++
		if strings.HasPrefix(current, ".") || segments > MaxFallbackSegments {
			return false
		}
	}
	return segments > 0
}

func bundleDigest(index asset, assets map[string]asset) string {
	keys := make([]string, 0, len(assets))
	for key := range assets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	_, _ = io.WriteString(hash, "/\x00"+index.etag+"\x00")
	for _, key := range keys {
		_, _ = io.WriteString(hash, key+"\x00"+assets[key].etag+"\x00")
	}
	return hex.EncodeToString(hash.Sum(nil))
}
