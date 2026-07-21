package documentrender_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
)

func TestContainerRendersFrozenPDFAndPPTXThroughHTTPContract(t *testing.T) {
	endpoint := os.Getenv("NANO_TEST_DOCUMENT_RENDERER_URL")
	if endpoint == "" {
		t.Skip("set NANO_TEST_DOCUMENT_RENDERER_URL to run the renderer container contract")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	adapter, err := documentrender.NewHTTPAdapter(documentrender.HTTPConfig{
		Endpoint: endpoint, ServiceToken: "nano-local-renderer-token", HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name, fixture string
		format        documentrender.Format
		pages         int
	}{
		{name: "pdf", fixture: "fixture://sprint6/pdf-en-v1", format: documentrender.FormatPDF, pages: 2},
		{name: "pptx", fixture: "fixture://sprint6/pptx-en-v1", format: documentrender.FormatPPTX, pages: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, err := rageval.ResolveFixture(testCase.fixture)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(fixture.Payload)
			request := documentrender.Request{
				SchemaVersion: 1, SourceID: "renderer-container-" + testCase.name, Format: testCase.format,
				InputSHA256: hex.EncodeToString(digest[:]), InputBytes: int64(len(fixture.Payload)),
				RenderConfigID: "pdfium-libreoffice-v1", MaxPages: 10, DPI: 144,
				MaxPixelsPerPage: 20_000_000, MaxOutputBytes: 32 << 20,
			}
			result, err := adapter.Render(context.Background(), request, fixture.Payload)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if len(result.Assets) != testCase.pages || len(result.Manifest.Pages) != testCase.pages {
				t.Fatalf("rendered pages = %d/%d, want %d", len(result.Assets), len(result.Manifest.Pages), testCase.pages)
			}
		})
	}
}
