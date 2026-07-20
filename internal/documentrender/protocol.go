package documentrender

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrRequestInvalid  = errors.New("document renderer Request is invalid")
	ErrManifestInvalid = errors.New("document renderer Manifest is invalid")
)

type Format string

const (
	FormatPDF  Format = "pdf"
	FormatPPTX Format = "pptx"
)

type Request struct {
	SchemaVersion    int    `json:"schema_version"`
	SourceID         string `json:"source_id"`
	Format           Format `json:"format"`
	InputSHA256      string `json:"input_sha256"`
	InputBytes       int64  `json:"input_bytes"`
	RenderConfigID   string `json:"render_config_id"`
	MaxPages         int    `json:"max_pages"`
	DPI              int    `json:"dpi"`
	MaxPixelsPerPage int64  `json:"max_pixels_per_page"`
	MaxOutputBytes   int64  `json:"max_output_bytes"`
}

func (r Request) Validate() error {
	if r.SchemaVersion != 1 || !boundedText(r.SourceID, 128) || !boundedText(r.RenderConfigID, 160) ||
		(r.Format != FormatPDF && r.Format != FormatPPTX) || !validSHA256(r.InputSHA256) || r.InputBytes < 1 ||
		r.MaxPages < 1 || r.MaxPages > 500 || r.DPI < 72 || r.DPI > 300 ||
		r.MaxPixelsPerPage < 1 || r.MaxPixelsPerPage > 100_000_000 || r.MaxOutputBytes < 1 || r.MaxOutputBytes > 2<<30 {
		return ErrRequestInvalid
	}
	return nil
}

type Manifest struct {
	SchemaVersion  int    `json:"schema_version"`
	SourceID       string `json:"source_id"`
	Format         Format `json:"format"`
	InputSHA256    string `json:"input_sha256"`
	RenderConfigID string `json:"render_config_id"`
	Pages          []Page `json:"pages"`
}

type Page struct {
	Ordinal   int    `json:"ordinal"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Filename  string `json:"filename"`
}

type Result struct {
	Manifest Manifest
	Assets   []Asset
}

type Asset struct {
	Page    Page
	Payload []byte
}

type Adapter interface {
	Render(context.Context, Request, []byte) (Result, error)
}

func Validate(request Request, manifest Manifest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if manifest.SchemaVersion != 1 || manifest.SourceID != request.SourceID || manifest.Format != request.Format ||
		manifest.InputSHA256 != request.InputSHA256 || manifest.RenderConfigID != request.RenderConfigID ||
		len(manifest.Pages) < 1 || len(manifest.Pages) > request.MaxPages {
		return ErrManifestInvalid
	}
	prefix := "page"
	if request.Format == FormatPPTX {
		prefix = "slide"
	}
	var totalBytes int64
	for index, page := range manifest.Pages {
		ordinal := index + 1
		expectedFilename := fmt.Sprintf("%s-%06d.png", prefix, ordinal)
		if page.Ordinal != ordinal || page.Width < 1 || page.Height < 1 || page.MediaType != "image/png" ||
			page.Bytes < 1 || !validSHA256(page.SHA256) || page.Filename != expectedFilename ||
			int64(page.Width) > request.MaxPixelsPerPage/int64(page.Height) {
			return ErrManifestInvalid
		}
		if totalBytes > request.MaxOutputBytes-page.Bytes {
			return ErrManifestInvalid
		}
		totalBytes += page.Bytes
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func boundedText(value string, maxRunes int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}
