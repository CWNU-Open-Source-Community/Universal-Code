// Package imageattachment validates inert operator image bytes. It does not
// fetch URLs, read filesystem paths, authorize tools, or interpret image text.
package imageattachment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	_ "golang.org/x/image/webp"
)

const MaxBytes = 5 * 1024 * 1024
const MaxDimension = 8192
const MaxPixels = 16 * 1024 * 1024

func Validate(content []byte, mimeType, name string) (domain.WorkspaceImage, error) {
	invalid := func(message string) (domain.WorkspaceImage, error) {
		return domain.WorkspaceImage{}, apperror.New(apperror.CodeInvalidArgument, message)
	}
	if len(content) == 0 || len(content) > MaxBytes {
		return invalid("Image must contain between 1 byte and 5 MiB")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 160 || strings.IndexFunc(name, unicode.IsControl) >= 0 || strings.ContainsAny(name, `/\\`) {
		return invalid("Image display name is invalid")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return invalid("Image cannot be decoded as PNG, JPEG, WebP, or GIF")
	}
	actualMIME := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp", "gif": "image/gif"}[format]
	if actualMIME == "" || actualMIME != mimeType {
		return invalid("Image MIME type does not match its decoded format")
	}
	if config.Width < 1 || config.Height < 1 || config.Width > MaxDimension || config.Height > MaxDimension || int64(config.Width)*int64(config.Height) > MaxPixels {
		return invalid("Image exceeds the 8192-pixel dimension or 16-megapixel limit")
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(content))
	if err != nil || decodedFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return invalid("Image data is incomplete or invalid")
	}
	digest := sha256.Sum256(content)
	return domain.WorkspaceImage{SHA256: hex.EncodeToString(digest[:]), MIMEType: actualMIME, ByteSize: len(content), Width: config.Width, Height: config.Height, Name: name}, nil
}
