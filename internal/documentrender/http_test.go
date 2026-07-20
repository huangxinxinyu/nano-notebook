package documentrender_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
)

func TestHTTPAdapterSendsBoundedIdentityAndVerifiesRenderedPNGArchive(t *testing.T) {
	payload := []byte("%PDF fixture")
	inputDigest := sha256.Sum256(payload)
	request := documentrender.Request{
		SchemaVersion: 1, SourceID: "src_http_pdf", Format: documentrender.FormatPDF,
		InputSHA256: hex.EncodeToString(inputDigest[:]), InputBytes: int64(len(payload)),
		RenderConfigID: "pdfium-7789-v1", MaxPages: 2, DPI: 144,
		MaxPixelsPerPage: 1_000_000, MaxOutputBytes: 1 << 20,
	}
	page := encodedPage(t, 320, 200)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/render" || r.Header.Get("Content-Type") != "application/pdf" ||
			r.Header.Get("X-Nano-Source-ID") != request.SourceID || r.Header.Get("X-Nano-Render-Config-ID") != request.RenderConfigID ||
			r.Header.Get("X-Nano-Input-SHA256") != request.InputSHA256 || r.Header.Get("X-Nano-Max-Pages") != "2" ||
			r.Header.Get("X-Nano-Render-DPI") != "144" {
			t.Errorf("renderer Request=%s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		if !bytes.Equal(body.Bytes(), payload) {
			t.Errorf("renderer body=%q", body.Bytes())
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(renderArchive(t, request, [][]byte{page}))
	}))
	defer server.Close()

	adapter, err := documentrender.NewHTTPAdapter(documentrender.HTTPConfig{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Render(context.Background(), request, payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(result.Assets) != 1 || !bytes.Equal(result.Assets[0].Payload, page) || result.Assets[0].Page.Width != 320 || result.Assets[0].Page.Height != 200 {
		t.Fatalf("Result=%#v", result)
	}
}

func TestHTTPAdapterRejectsArchiveTraversalAndPNGIdentityDrift(t *testing.T) {
	payload := []byte("%PDF fixture")
	digest := sha256.Sum256(payload)
	request := documentrender.Request{SchemaVersion: 1, SourceID: "src", Format: documentrender.FormatPDF, InputSHA256: hex.EncodeToString(digest[:]), InputBytes: int64(len(payload)), RenderConfigID: "v1", MaxPages: 1, DPI: 144, MaxPixelsPerPage: 1_000_000, MaxOutputBytes: 1 << 20}
	page := encodedPage(t, 10, 10)
	for name, archive := range map[string][]byte{
		"traversal":        rawRenderArchive(t, map[string][]byte{"../page.png": page}),
		"missing_manifest": rawRenderArchive(t, map[string][]byte{"page-000001.png": page}),
		"dimension_drift":  renderArchiveWithDimensions(t, request, page, 11, 10),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write(archive)
			}))
			defer server.Close()
			adapter, _ := documentrender.NewHTTPAdapter(documentrender.HTTPConfig{Endpoint: server.URL, HTTPClient: server.Client()})
			if _, err := adapter.Render(context.Background(), request, payload); err == nil {
				t.Fatal("accepted invalid renderer archive")
			}
		})
	}
}

func encodedPage(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	value.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func renderArchive(t *testing.T, request documentrender.Request, pages [][]byte) []byte {
	t.Helper()
	assets := make([]documentrender.Page, len(pages))
	files := make(map[string][]byte, len(pages)+1)
	for index, payload := range pages {
		digest := sha256.Sum256(payload)
		prefix := "page"
		if request.Format == documentrender.FormatPPTX {
			prefix = "slide"
		}
		name := fmt.Sprintf("%s-%06d.png", prefix, index+1)
		assets[index] = documentrender.Page{Ordinal: index + 1, Width: 320, Height: 200, MediaType: "image/png", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]), Filename: name}
		files[name] = payload
	}
	manifest := documentrender.Manifest{SchemaVersion: 1, SourceID: request.SourceID, Format: request.Format, InputSHA256: request.InputSHA256, RenderConfigID: request.RenderConfigID, Pages: assets}
	encoded, _ := json.Marshal(manifest)
	files["manifest.json"] = encoded
	return rawRenderArchive(t, files)
}

func renderArchiveWithDimensions(t *testing.T, request documentrender.Request, page []byte, width, height int) []byte {
	t.Helper()
	digest := sha256.Sum256(page)
	manifest := documentrender.Manifest{SchemaVersion: 1, SourceID: request.SourceID, Format: request.Format, InputSHA256: request.InputSHA256, RenderConfigID: request.RenderConfigID, Pages: []documentrender.Page{{Ordinal: 1, Width: width, Height: height, MediaType: "image/png", Bytes: int64(len(page)), SHA256: hex.EncodeToString(digest[:]), Filename: "page-000001.png"}}}
	encoded, _ := json.Marshal(manifest)
	return rawRenderArchive(t, map[string][]byte{"manifest.json": encoded, "page-000001.png": page})
}

func rawRenderArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for name, payload := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
