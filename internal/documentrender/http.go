package documentrender

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

type HTTPConfig struct {
	Endpoint     string
	ServiceToken string
	HTTPClient   *http.Client
}

type HTTPAdapter struct {
	endpoint     string
	serviceToken string
	client       *http.Client
}

func NewHTTPAdapter(config HTTPConfig) (*HTTPAdapter, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.Endpoint), "/"))
	config.ServiceToken = strings.TrimSpace(config.ServiceToken)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || config.ServiceToken == "" || config.HTTPClient == nil {
		return nil, errors.New("document renderer HTTP Adapter configuration is invalid")
	}
	return &HTTPAdapter{endpoint: parsed.String(), serviceToken: config.ServiceToken, client: config.HTTPClient}, nil
}

func (a *HTTPAdapter) Render(ctx context.Context, request Request, input []byte) (Result, error) {
	if a == nil || a.client == nil {
		return Result{}, errors.New("nil document renderer HTTP Adapter")
	}
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	digest := sha256.Sum256(input)
	if int64(len(input)) != request.InputBytes || hex.EncodeToString(digest[:]) != request.InputSHA256 {
		return Result{}, ErrRequestInvalid
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/v1/render", bytes.NewReader(input))
	if err != nil {
		return Result{}, err
	}
	httpRequest.Header.Set("Content-Type", map[Format]string{FormatPDF: "application/pdf", FormatPPTX: "application/vnd.openxmlformats-officedocument.presentationml.presentation"}[request.Format])
	httpRequest.Header.Set("Authorization", "Bearer "+a.serviceToken)
	httpRequest.Header.Set("X-Nano-Source-ID", request.SourceID)
	httpRequest.Header.Set("X-Nano-Input-SHA256", request.InputSHA256)
	httpRequest.Header.Set("X-Nano-Render-Config-ID", request.RenderConfigID)
	httpRequest.Header.Set("X-Nano-Max-Pages", strconv.Itoa(request.MaxPages))
	httpRequest.Header.Set("X-Nano-Render-DPI", strconv.Itoa(request.DPI))
	httpRequest.Header.Set("X-Nano-Max-Pixels-Per-Page", strconv.FormatInt(request.MaxPixelsPerPage, 10))
	httpRequest.Header.Set("X-Nano-Max-Output-Bytes", strconv.FormatInt(request.MaxOutputBytes, 10))
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/zip") {
		return Result{}, fmt.Errorf("document renderer returned status %d", response.StatusCode)
	}
	archiveLimit := request.MaxOutputBytes + 1<<20
	encoded, err := io.ReadAll(io.LimitReader(response.Body, archiveLimit+1))
	if err != nil || int64(len(encoded)) > archiveLimit {
		return Result{}, ErrManifestInvalid
	}
	return decodeArchive(request, encoded)
}

func decodeArchive(request Request, encoded []byte) (Result, error) {
	archive, err := zip.NewReader(bytes.NewReader(encoded), int64(len(encoded)))
	if err != nil || len(archive.File) < 2 || len(archive.File) > request.MaxPages+1 {
		return Result{}, ErrManifestInvalid
	}
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if file.FileInfo().IsDir() || file.Name != path.Base(file.Name) || path.Clean(file.Name) != file.Name {
			return Result{}, ErrManifestInvalid
		}
		if _, duplicate := files[file.Name]; duplicate {
			return Result{}, ErrManifestInvalid
		}
		files[file.Name] = file
	}
	manifestFile := files["manifest.json"]
	if manifestFile == nil || manifestFile.UncompressedSize64 > 1<<20 {
		return Result{}, ErrManifestInvalid
	}
	manifestPayload, err := readZipFile(manifestFile, 1<<20)
	if err != nil {
		return Result{}, ErrManifestInvalid
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestPayload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Result{}, ErrManifestInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Result{}, ErrManifestInvalid
	}
	if err := Validate(request, manifest); err != nil {
		return Result{}, err
	}
	result := Result{Manifest: manifest, Assets: make([]Asset, 0, len(manifest.Pages))}
	for _, page := range manifest.Pages {
		file := files[page.Filename]
		if file == nil || file.UncompressedSize64 != uint64(page.Bytes) {
			return Result{}, ErrManifestInvalid
		}
		payload, err := readZipFile(file, page.Bytes)
		if err != nil {
			return Result{}, ErrManifestInvalid
		}
		digest := sha256.Sum256(payload)
		configuration, err := png.DecodeConfig(bytes.NewReader(payload))
		if err != nil || hex.EncodeToString(digest[:]) != page.SHA256 || configuration.Width != page.Width || configuration.Height != page.Height {
			return Result{}, ErrManifestInvalid
		}
		result.Assets = append(result.Assets, Asset{Page: page, Payload: payload})
		delete(files, page.Filename)
	}
	delete(files, "manifest.json")
	if len(files) != 0 {
		return Result{}, ErrManifestInvalid
	}
	return result, nil
}

func readZipFile(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(payload)) > limit {
		return nil, ErrManifestInvalid
	}
	return payload, nil
}
