package main

import (
	"testing"
	"time"
)

func TestLoadRendererConfigReadsExplicitResourceAndBinaryBounds(t *testing.T) {
	t.Setenv("NANO_DOCUMENT_RENDERER_ADDR", "127.0.0.1:18084")
	t.Setenv("NANO_DOCUMENT_RENDERER_SERVICE_TOKEN", "service-token")
	t.Setenv("NANO_DOCUMENT_RENDER_CONFIG_ID", "pdfium-lo-v7")
	t.Setenv("NANO_PDFIUM_BINARY", "/opt/pdfium/pdfium_test")
	t.Setenv("NANO_LIBREOFFICE_BINARY", "/usr/bin/soffice")
	t.Setenv("NANO_DOCUMENT_RENDERER_SCRATCH_ROOT", "/var/tmp/nano-renderer")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_RUNTIME", "45s")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_INPUT_BYTES", "1048576")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_CONVERTED_PDF_BYTES", "2097152")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_PAGES", "25")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_PIXELS_PER_PAGE", "3000000")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_OUTPUT_BYTES", "4194304")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_CONCURRENT", "3")

	config, err := loadRendererConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Addr != "127.0.0.1:18084" || config.ServiceToken != "service-token" || config.RenderConfigID != "pdfium-lo-v7" ||
		config.PDFiumBinary != "/opt/pdfium/pdfium_test" || config.LibreOfficeBinary != "/usr/bin/soffice" ||
		config.ScratchRoot != "/var/tmp/nano-renderer" || config.MaxRuntime != 45*time.Second || config.MaxInputBytes != 1<<20 ||
		config.MaxConvertedPDFBytes != 2<<20 || config.MaxPages != 25 || config.MaxPixelsPerPage != 3_000_000 ||
		config.MaxOutputBytes != 4<<20 || config.MaxConcurrent != 3 {
		t.Fatalf("config=%+v", config)
	}
}

func TestLoadRendererConfigRejectsInconsistentBounds(t *testing.T) {
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_PAGES", "0")
	if _, err := loadRendererConfig(); err == nil {
		t.Fatal("accepted zero page budget")
	}
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_PAGES", "500")
	t.Setenv("NANO_DOCUMENT_RENDERER_MAX_RUNTIME", "invalid")
	if _, err := loadRendererConfig(); err == nil {
		t.Fatal("accepted invalid runtime")
	}
}
