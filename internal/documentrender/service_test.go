package documentrender_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
)

type recordingRenderer struct {
	request documentrender.Request
	payload []byte
	result  documentrender.Result
	err     error
}

func (r *recordingRenderer) Render(_ context.Context, request documentrender.Request, payload []byte) (documentrender.Result, error) {
	r.request = request
	r.payload = append([]byte(nil), payload...)
	return r.result, r.err
}

func TestServiceAcceptsAuthenticatedBoundedRenderAndReturnsVerifiedArchive(t *testing.T) {
	payload := []byte("%PDF fixture")
	inputDigest := sha256.Sum256(payload)
	page := encodedPage(t, 320, 200)
	pageDigest := sha256.Sum256(page)
	manifest := documentrender.Manifest{
		SchemaVersion: 1, SourceID: "src_service", Format: documentrender.FormatPDF,
		InputSHA256: hex.EncodeToString(inputDigest[:]), RenderConfigID: "pdfium-v1",
		Pages: []documentrender.Page{{Ordinal: 1, Width: 320, Height: 200, MediaType: "image/png", Bytes: int64(len(page)), SHA256: hex.EncodeToString(pageDigest[:]), Filename: "page-000001.png"}},
	}
	renderer := &recordingRenderer{result: documentrender.Result{Manifest: manifest, Assets: []documentrender.Asset{{Page: manifest.Pages[0], Payload: page}}}}
	handler, err := documentrender.NewServiceHandler(renderer, documentrender.ServiceConfig{
		ServiceToken: "renderer-token", RenderConfigID: "pdfium-v1", MaxInputBytes: 1 << 20,
		MaxPages: 10, MaxPixelsPerPage: 2_000_000, MaxOutputBytes: 4 << 20, MaxConcurrent: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	adapter, err := documentrender.NewHTTPAdapter(documentrender.HTTPConfig{Endpoint: server.URL, ServiceToken: "renderer-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	request := documentrender.Request{
		SchemaVersion: 1, SourceID: "src_service", Format: documentrender.FormatPDF,
		InputSHA256: hex.EncodeToString(inputDigest[:]), InputBytes: int64(len(payload)), RenderConfigID: "pdfium-v1",
		MaxPages: 2, DPI: 144, MaxPixelsPerPage: 1_000_000, MaxOutputBytes: 1 << 20,
	}
	result, err := adapter.Render(context.Background(), request, payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(renderer.payload, payload) || renderer.request != request || len(result.Assets) != 1 || !bytes.Equal(result.Assets[0].Payload, page) {
		t.Fatalf("service round trip request=%+v result=%+v", renderer.request, result)
	}
}

func TestServiceRejectsMissingAuthenticationAndOversizedInputBeforeRendering(t *testing.T) {
	renderer := &recordingRenderer{}
	handler, err := documentrender.NewServiceHandler(renderer, documentrender.ServiceConfig{
		ServiceToken: "renderer-token", RenderConfigID: "pdfium-v1", MaxInputBytes: 4,
		MaxPages: 10, MaxPixelsPerPage: 2_000_000, MaxOutputBytes: 4 << 20, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, authorization := range map[string]string{"missing": "", "wrong": "Bearer wrong"} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/render", bytes.NewReader([]byte("pdf")))
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/render", bytes.NewReader([]byte("large")))
	request.Header.Set("Authorization", "Bearer renderer-token")
	request.Header.Set("Content-Type", "application/pdf")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || renderer.payload != nil {
		t.Fatalf("oversized status=%d payload=%q", response.Code, renderer.payload)
	}
}
