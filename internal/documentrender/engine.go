package documentrender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pdfreader "github.com/ledongthuc/pdf"
)

type CommandRunner interface {
	Run(context.Context, string, string, ...string) error
}

type EngineConfig struct {
	RenderConfigID       string
	PDFiumBinary         string
	LibreOfficeBinary    string
	ScratchRoot          string
	MaxRuntime           time.Duration
	MaxConvertedPDFBytes int64
	Runner               CommandRunner
}

type Engine struct {
	config EngineConfig
}

func NewEngine(config EngineConfig) (*Engine, error) {
	config.RenderConfigID = strings.TrimSpace(config.RenderConfigID)
	config.PDFiumBinary = strings.TrimSpace(config.PDFiumBinary)
	config.LibreOfficeBinary = strings.TrimSpace(config.LibreOfficeBinary)
	config.ScratchRoot = strings.TrimSpace(config.ScratchRoot)
	if config.RenderConfigID == "" || config.PDFiumBinary == "" || config.LibreOfficeBinary == "" || config.ScratchRoot == "" ||
		config.MaxRuntime <= 0 || config.MaxRuntime > 10*time.Minute || config.MaxConvertedPDFBytes < 1 || config.MaxConvertedPDFBytes > 1<<30 || config.Runner == nil {
		return nil, errors.New("document renderer Engine configuration is invalid")
	}
	return &Engine{config: config}, nil
}

func (e *Engine) Render(ctx context.Context, request Request, payload []byte) (Result, error) {
	if e == nil || e.config.Runner == nil || request.RenderConfigID != e.config.RenderConfigID {
		return Result{}, ErrRequestInvalid
	}
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	digest := sha256.Sum256(payload)
	if int64(len(payload)) != request.InputBytes || hex.EncodeToString(digest[:]) != request.InputSHA256 {
		return Result{}, ErrRequestInvalid
	}
	renderContext, cancel := context.WithTimeout(ctx, e.config.MaxRuntime)
	defer cancel()
	directory, err := os.MkdirTemp(e.config.ScratchRoot, "nano-render-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(directory)

	inputExtension := ".pdf"
	if request.Format == FormatPPTX {
		inputExtension = ".pptx"
	}
	inputPath := filepath.Join(directory, "input"+inputExtension)
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		return Result{}, err
	}
	pdfPath := inputPath
	if request.Format == FormatPPTX {
		profileURL := (&url.URL{Scheme: "file", Path: filepath.Join(directory, "libreoffice-profile")}).String()
		if err := e.config.Runner.Run(renderContext, directory, e.config.LibreOfficeBinary,
			"--headless", "--nologo", "--nodefault", "--nolockcheck", "--nofirststartwizard",
			"-env:UserInstallation="+profileURL, "--convert-to", "pdf", "--outdir", directory, inputPath); err != nil {
			return Result{}, errors.New("PPTX conversion failed")
		}
		pdfPath = filepath.Join(directory, "input.pdf")
		if err := validateRegularFile(pdfPath, e.config.MaxConvertedPDFBytes); err != nil {
			return Result{}, errors.New("PPTX conversion output is invalid")
		}
	}
	pageCount, err := boundedPDFPageCount(pdfPath, e.config.MaxConvertedPDFBytes)
	if err != nil || pageCount < 1 || pageCount > request.MaxPages {
		return Result{}, ErrRequestInvalid
	}
	scale := strconv.FormatFloat(float64(request.DPI)/72, 'f', -1, 64)
	maxPixels := strconv.FormatInt(request.MaxPixelsPerPage, 10)
	if err := e.config.Runner.Run(renderContext, directory, e.config.PDFiumBinary, "--png", "--scale="+scale, "--max-pixels="+maxPixels, pdfPath); err != nil {
		return Result{}, errors.New("PDF rasterization failed")
	}
	return e.collect(request, pdfPath, pageCount)
}

func (e *Engine) collect(request Request, pdfPath string, pageCount int) (Result, error) {
	matches, err := filepath.Glob(pdfPath + ".*.png")
	if err != nil || len(matches) != pageCount {
		return Result{}, ErrManifestInvalid
	}
	manifest := Manifest{
		SchemaVersion: 1, SourceID: request.SourceID, Format: request.Format,
		InputSHA256: request.InputSHA256, RenderConfigID: request.RenderConfigID,
		Pages: make([]Page, 0, pageCount),
	}
	result := Result{Assets: make([]Asset, 0, pageCount)}
	prefix := "page"
	if request.Format == FormatPPTX {
		prefix = "slide"
	}
	remaining := request.MaxOutputBytes
	for index := 0; index < pageCount; index++ {
		path := fmt.Sprintf("%s.%d.png", pdfPath, index)
		if err := validateRegularFile(path, remaining); err != nil {
			return Result{}, ErrManifestInvalid
		}
		payload, err := readRegularFile(path, remaining)
		if err != nil {
			return Result{}, ErrManifestInvalid
		}
		configuration, err := png.DecodeConfig(bytes.NewReader(payload))
		if err != nil || configuration.Width < 1 || configuration.Height < 1 ||
			int64(configuration.Width) > request.MaxPixelsPerPage/int64(configuration.Height) {
			return Result{}, ErrManifestInvalid
		}
		digest := sha256.Sum256(payload)
		page := Page{
			Ordinal: index + 1, Width: configuration.Width, Height: configuration.Height,
			MediaType: "image/png", Bytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
			Filename: fmt.Sprintf("%s-%06d.png", prefix, index+1),
		}
		remaining -= page.Bytes
		manifest.Pages = append(manifest.Pages, page)
		result.Assets = append(result.Assets, Asset{Page: page, Payload: payload})
	}
	result.Manifest = manifest
	if err := Validate(request, manifest); err != nil {
		return Result{}, err
	}
	return result, nil
}

func boundedPDFPageCount(path string, limit int64) (count int, err error) {
	if err := validateRegularFile(path, limit); err != nil {
		return 0, err
	}
	payload, err := readRegularFile(path, limit)
	if err != nil {
		return 0, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			count, err = 0, errors.New("PDF parser failed")
		}
	}()
	reader, err := pdfreader.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return 0, err
	}
	return reader.NumPage(), nil
}

func validateRegularFile(path string, limit int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return errors.New("render output file is invalid")
	}
	return nil
}

func readRegularFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(payload)) > limit {
		return nil, ErrManifestInvalid
	}
	return payload, nil
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, directory, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	output := &boundedCommandOutput{remaining: 64 << 10}
	command.Stdout = output
	command.Stderr = output
	return command.Run()
}

type boundedCommandOutput struct {
	remaining int
}

func (w *boundedCommandOutput) Write(payload []byte) (int, error) {
	written := len(payload)
	if len(payload) > w.remaining {
		payload = payload[:w.remaining]
	}
	w.remaining -= len(payload)
	return written, nil
}
