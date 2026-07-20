package rageval_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"context"
	"github.com/huangxinxinyu/nano-notebook/internal/models"
	"github.com/huangxinxinyu/nano-notebook/internal/normalize"
	"github.com/huangxinxinyu/nano-notebook/internal/rageval"
	"github.com/huangxinxinyu/nano-notebook/internal/source"
	"github.com/huangxinxinyu/nano-notebook/internal/sourceprocessing"
)

func TestSprint6SuiteIsStrictValidAndDigestPinned(t *testing.T) {
	payload, err := os.ReadFile("../../evals/rag/sprint6-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var suite rageval.Suite
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		t.Fatal(err)
	}
	if err := suite.Validate(); err != nil {
		t.Fatal(err)
	}
	extractor := sourceprocessing.NewNativeExtractor(evalMediaStub{}, sourceprocessing.NativeExtractorConfig{
		VisionModel: "vision-controlled", VisionPromptVersion: "vision-v1", TranscriptionModel: "transcription-controlled",
	})
	for _, evalCase := range suite.Cases {
		for _, fixture := range evalCase.Fixtures {
			resolved, resolveErr := rageval.ResolveFixture(fixture.URI)
			if resolveErr != nil {
				t.Errorf("ResolveFixture(%s): %v", fixture.URI, resolveErr)
				continue
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(resolved.Payload))
			if resolved.Family != fixture.Family || digest != fixture.SHA256 {
				t.Errorf("fixture %s family/SHA = %s/%s", fixture.ID, resolved.Family, digest)
			}
			artifact, extractErr := extractor.Extract(context.Background(), source.Source{
				ID: fixture.ID, Title: resolved.Filename, Format: source.Format(resolved.Family), MediaType: resolved.MediaType,
			}, resolved.Payload, "nano-native-extract-v1")
			if extractErr != nil || normalize.Validate(artifact) != nil {
				t.Errorf("production Extractor rejected fixture %s: %v", fixture.ID, extractErr)
			}
		}
	}
	digest, err := suite.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if digest != "bf7f7e3e558ef5bb1bddc516375d0a13b93edf910902bce90881f1d9e8c65b4d" {
		t.Fatalf("suite SHA-256 = %s", digest)
	}
}

type evalMediaStub struct{}

func (evalMediaStub) Transcribe(context.Context, models.TranscriptionRequest) (models.TranscriptionOutcome, error) {
	return models.TranscriptionOutcome{Segments: []models.TranscriptSegment{{StartMS: 0, EndMS: 2000, Text: "Controlled audio evidence."}}}, nil
}

func (evalMediaStub) DescribeImage(_ context.Context, request models.VisionRequest) (models.VisionOutcome, error) {
	return models.VisionOutcome{Regions: []models.VisionRegion{{Text: "Controlled image evidence.", Width: float64(request.Width), Height: float64(request.Height)}}}, nil
}
