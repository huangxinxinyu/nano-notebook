package documentrender_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huangxinxinyu/nano-notebook/internal/documentrender"
)

type commandCall struct {
	dir  string
	name string
	args []string
}

type fakeCommandRunner struct {
	calls []commandCall
	run   func(context.Context, string, string, ...string) error
}

func (r *fakeCommandRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	r.calls = append(r.calls, commandCall{dir: dir, name: name, args: append([]string(nil), args...)})
	if r.run != nil {
		return r.run(ctx, dir, name, args...)
	}
	return nil
}

func TestEngineInvokesPinnedPDFiumWithoutShellAndReturnsOrderedPages(t *testing.T) {
	payload := rendererPDF("one", "two")
	pageOne := encodedPage(t, 612, 792)
	pageTwo := encodedPage(t, 300, 400)
	runner := &fakeCommandRunner{run: func(_ context.Context, dir, name string, args ...string) error {
		if name != "/opt/pdfium/pdfium_test" || len(args) != 4 || args[0] != "--png" || args[1] != "--scale=2" || args[2] != "--max-pixels=2000000" || args[3] != filepath.Join(dir, "input.pdf") {
			return fmt.Errorf("unexpected command %q %q", name, args)
		}
		if err := os.WriteFile(filepath.Join(dir, "input.pdf.1.png"), pageTwo, 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "input.pdf.0.png"), pageOne, 0o600)
	}}
	engine, err := documentrender.NewEngine(documentrender.EngineConfig{
		RenderConfigID: "pdfium-v1", PDFiumBinary: "/opt/pdfium/pdfium_test", LibreOfficeBinary: "/usr/bin/soffice",
		ScratchRoot: t.TempDir(), MaxRuntime: time.Second, MaxConvertedPDFBytes: 8 << 20, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Render(context.Background(), renderRequest(payload, documentrender.FormatPDF, "pdfium-v1", 2), payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(runner.calls) != 1 || len(result.Assets) != 2 || result.Assets[0].Page.Filename != "page-000001.png" ||
		result.Assets[1].Page.Filename != "page-000002.png" || !bytes.Equal(result.Assets[0].Payload, pageOne) || !bytes.Equal(result.Assets[1].Payload, pageTwo) {
		t.Fatalf("calls=%+v result=%+v", runner.calls, result)
	}
}

func TestEngineRejectsEncryptedOrMalformedPDFBeforeExecutingPDFium(t *testing.T) {
	payload := rendererPDF("secret")
	payload = bytes.Replace(payload, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt 3 0 R"), 1)
	runner := &fakeCommandRunner{}
	engine, err := documentrender.NewEngine(documentrender.EngineConfig{
		RenderConfigID: "pdfium-v1", PDFiumBinary: "pdfium_render", LibreOfficeBinary: "soffice", ScratchRoot: t.TempDir(),
		MaxRuntime: time.Second, MaxConvertedPDFBytes: 8 << 20, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Render(context.Background(), renderRequest(payload, documentrender.FormatPDF, "pdfium-v1", 1), payload); err == nil {
		t.Fatal("accepted encrypted or malformed PDF")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("executed PDFium before encrypted PDF rejection: %+v", runner.calls)
	}
}

func TestEngineRejectsKnownPageOverflowBeforeExecutingPDFium(t *testing.T) {
	payload := rendererPDF("one", "two")
	runner := &fakeCommandRunner{}
	engine, err := documentrender.NewEngine(documentrender.EngineConfig{
		RenderConfigID: "pdfium-v1", PDFiumBinary: "pdfium_test", LibreOfficeBinary: "soffice", ScratchRoot: t.TempDir(),
		MaxRuntime: time.Second, MaxConvertedPDFBytes: 8 << 20, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Render(context.Background(), renderRequest(payload, documentrender.FormatPDF, "pdfium-v1", 1), payload); err == nil {
		t.Fatal("accepted PDF above page budget")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("executed commands before page budget check: %+v", runner.calls)
	}
}

func TestEngineConvertsPPTXWithJobScopedProfileThenRendersSlides(t *testing.T) {
	payload := []byte("pptx fixture")
	converted := rendererPDF("slide")
	page := encodedPage(t, 640, 360)
	runner := &fakeCommandRunner{run: func(_ context.Context, dir, name string, args ...string) error {
		switch name {
		case "/usr/bin/soffice":
			if !containsArgument(args, "--headless") || !containsArgument(args, "--convert-to") || !containsArgument(args, "pdf") ||
				!containsArgument(args, "--outdir") || !containsArgument(args, dir) || !containsArgumentPrefix(args, "-env:UserInstallation=file://") {
				return fmt.Errorf("unsafe LibreOffice args %q", args)
			}
			return os.WriteFile(filepath.Join(dir, "input.pdf"), converted, 0o600)
		case "/opt/pdfium/pdfium_test":
			return os.WriteFile(filepath.Join(dir, "input.pdf.0.png"), page, 0o600)
		default:
			return fmt.Errorf("unexpected command %q", name)
		}
	}}
	engine, err := documentrender.NewEngine(documentrender.EngineConfig{
		RenderConfigID: "office-pdfium-v1", PDFiumBinary: "/opt/pdfium/pdfium_test", LibreOfficeBinary: "/usr/bin/soffice",
		ScratchRoot: t.TempDir(), MaxRuntime: time.Second, MaxConvertedPDFBytes: 8 << 20, Runner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Render(context.Background(), renderRequest(payload, documentrender.FormatPPTX, "office-pdfium-v1", 1), payload)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(runner.calls) != 2 || len(result.Assets) != 1 || result.Assets[0].Page.Filename != "slide-000001.png" {
		t.Fatalf("calls=%+v result=%+v", runner.calls, result)
	}
}

func renderRequest(payload []byte, format documentrender.Format, configID string, maxPages int) documentrender.Request {
	digest := sha256.Sum256(payload)
	return documentrender.Request{
		SchemaVersion: 1, SourceID: "src_render", Format: format, InputSHA256: hex.EncodeToString(digest[:]), InputBytes: int64(len(payload)),
		RenderConfigID: configID, MaxPages: maxPages, DPI: 144, MaxPixelsPerPage: 2_000_000, MaxOutputBytes: 4 << 20,
	}
}

func containsArgument(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsArgumentPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func rendererPDF(pageTexts ...string) []byte {
	objects := make([]string, 3+2*len(pageTexts))
	objects[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	kids := make([]string, 0, len(pageTexts))
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+index*2))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	objects[2] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"
	for index, value := range pageTexts {
		pageObject := 4 + index*2
		contentObject := pageObject + 1
		objects[pageObject-1] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObject)
		content := "BT /F1 12 Tf 72 720 Td (" + value + ") Tj ET"
		objects[contentObject-1] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return output.Bytes()
}
