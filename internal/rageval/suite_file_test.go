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
	"github.com/huangxinxinyu/nano-notebook/internal/objectstore"
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
	if digest != "f48f765dfbb70ad1debdc5ca83879d8029dcc561ec1aa5ddc32b253bceb1977c" {
		t.Fatalf("suite SHA-256 = %s", digest)
	}
}

func TestModelMediaFixturesContainEncodedVisualOrSpokenEvidence(t *testing.T) {
	for _, testCase := range []struct {
		uri, format, fact string
		image             bool
	}{
		{uri: "fixture://sprint6/mp3-en-v1", format: "mp3", fact: "interactive queue is reserved"},
		{uri: "fixture://sprint6/wav-en-v1", format: "wav", fact: "reranker unavailable"},
		{uri: "fixture://sprint6/m4a-en-v1", format: "m4a", fact: "PostgreSQL is authority"},
		{uri: "fixture://sprint6/png-en-v1", format: "png", fact: "publication barrier", image: true},
		{uri: "fixture://sprint6/jpeg-en-v1", format: "jpeg", fact: "verifier", image: true},
		{uri: "fixture://sprint6/webp-en-v1", format: "webp", fact: "Ready", image: true},
	} {
		t.Run(testCase.format, func(t *testing.T) {
			fixture, err := rageval.ResolveFixture(testCase.uri)
			if err != nil {
				t.Fatal(err)
			}
			minimumBytes := 4 << 10
			if testCase.image {
				minimumBytes = 1 << 10
			}
			if len(fixture.Payload) < minimumBytes {
				t.Fatalf("fixture is only a structural stub: %d bytes", len(fixture.Payload))
			}
			if bytes.Contains(bytes.ToLower(fixture.Payload), bytes.ToLower([]byte(testCase.fact))) {
				t.Fatal("required fact is hidden as plaintext metadata instead of encoded media")
			}
			if err := objectstore.ValidateSourceContent(testCase.format, bytes.NewReader(fixture.Payload), int64(len(fixture.Payload))); err != nil {
				t.Fatalf("content sniff: %v", err)
			}
			if testCase.image {
				width, height, err := normalize.ImageDimensions(testCase.format, fixture.Payload)
				if err != nil || width < 600 || height < 300 {
					t.Fatalf("model-readable image geometry = %dx%d, %v", width, height, err)
				}
			}
		})
	}
}

type evalMediaStub struct{}

func (evalMediaStub) Transcribe(context.Context, models.TranscriptionRequest) (models.TranscriptionOutcome, error) {
	return models.TranscriptionOutcome{Segments: []models.TranscriptSegment{{StartMS: 0, EndMS: 2000, Text: "Controlled audio evidence."}}}, nil
}

func (evalMediaStub) DescribeImage(_ context.Context, request models.VisionRequest) (models.VisionOutcome, error) {
	return models.VisionOutcome{Regions: []models.VisionRegion{{Text: "Controlled image evidence.", Width: float64(request.Width), Height: float64(request.Height)}}}, nil
}
