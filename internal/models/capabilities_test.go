package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestBifrostTranscriptionReturnsTimestampedProviderNeutralSegments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("X-Request-ID") == "" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := io.ReadAll(file)
		if r.FormValue("model") != "openai/whisper-1" || r.FormValue("response_format") != "verbose_json" ||
			header.Filename != "source.wav" || string(payload) != "RIFFaudio" {
			t.Fatalf("form model=%q format=%q file=%q payload=%q", r.FormValue("model"), r.FormValue("response_format"), header.Filename, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"text":"First. Second.","segments":[
				{"id":0,"start":0.0,"end":1.25,"text":" First. "},
				{"id":1,"start":1.25,"end":2.5,"text":"Second."}
			],"usage":{"input_tokens":10,"total_tokens":14},
			"extra_fields":{"provider":"openai","model_requested":"openai/whisper-1","model_deployment":"whisper-1"}
		}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	outcome, err := client.Transcribe(context.Background(), TranscriptionRequest{
		Model: "openai/whisper-1", Filename: "source.wav", MediaType: "audio/wav", Audio: []byte("RIFFaudio"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.Segments, []TranscriptSegment{
		{StartMS: 0, EndMS: 1250, Text: "First."}, {StartMS: 1250, EndMS: 2500, Text: "Second."},
	}) || outcome.Metadata.Provider != "openai" || outcome.Metadata.Model != "whisper-1" {
		t.Fatalf("transcription outcome=%+v", outcome)
	}
}

func TestBifrostVisionReturnsOnlyBoundedImageRegions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "gemini/gemini-2.5-flash" || len(request.Messages) != 2 ||
			!strings.Contains(string(request.Messages[1].Content), "data:image/png;base64,aW1hZ2U=") {
			t.Fatalf("vision request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"provider":"gemini","model":"gemini-2.5-flash",
			"choices":[{"message":{"role":"assistant","content":"{\"regions\":[{\"text\":\"Chart: revenue rose.\",\"x\":10,\"y\":20,\"width\":100,\"height\":50}]}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"total_tokens":30}
		}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	outcome, err := client.DescribeImage(context.Background(), VisionRequest{
		Model: "gemini/gemini-2.5-flash", MediaType: "image/png", Image: []byte("image"), Width: 320, Height: 200,
		PromptVersion: "vision-normalize-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.Regions, []VisionRegion{{Text: "Chart: revenue rose.", X: 10, Y: 20, Width: 100, Height: 50}}) ||
		outcome.Metadata.Provider != "gemini" {
		t.Fatalf("vision outcome=%+v", outcome)
	}
}

func TestBifrostMediaCapabilitiesRejectUnboundedProviderResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/audio/transcriptions" {
			_, _ = w.Write([]byte(`{"segments":[{"start":2,"end":1,"text":"backwards"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"regions\":[{\"text\":\"outside\",\"x\":90,\"y\":0,\"width\":20,\"height\":10}]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	if _, err := client.Transcribe(context.Background(), TranscriptionRequest{Model: "m", Filename: "a.mp3", MediaType: "audio/mpeg", Audio: []byte("audio")}); err == nil {
		t.Fatal("Transcribe accepted a backwards interval")
	}
	if _, err := client.DescribeImage(context.Background(), VisionRequest{
		Model: "m", MediaType: "image/png", Image: []byte("image"), Width: 100, Height: 100, PromptVersion: "v1",
	}); err == nil {
		t.Fatal("DescribeImage accepted an out-of-bounds region")
	}
}

func TestBifrostEmbeddingReturnsProviderNeutralOrderedVectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.Header.Get("X-Request-ID") == "" {
			t.Fatalf("request=%s %s request-id=%q", r.Method, r.URL.Path, r.Header.Get("X-Request-ID"))
		}
		var request struct {
			Model      string   `json:"model"`
			Input      []string `json:"input"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "openai/text-embedding-3-small" || !reflect.DeepEqual(request.Input, []string{"first", "second"}) || request.Dimensions != 3 {
			t.Fatalf("embedding request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"provider":"openai","model":"text-embedding-3-small",
			"data":[{"index":1,"embedding":[0,1,0]},{"index":0,"embedding":[1,0,0]}],
			"usage":{"prompt_tokens":4,"total_tokens":4},"cost":0.0001,"cost_currency":"USD","cost_source":"provider"
		}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	outcome, err := client.Embed(context.Background(), EmbeddingRequest{
		Model: "openai/text-embedding-3-small", Inputs: []string{"first", "second"}, Dimensions: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.Vectors, [][]float32{{1, 0, 0}, {0, 1, 0}}) || outcome.Metadata.Provider != "openai" ||
		outcome.Metadata.Model != "text-embedding-3-small" || outcome.Metadata.InputTokens == nil || *outcome.Metadata.InputTokens != 4 || !outcome.Metadata.Cost.Known {
		t.Fatalf("embedding outcome=%+v", outcome)
	}
}

func TestBifrostRerankerReturnsCandidateIdentitiesWithoutProviderDocuments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/rerank" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Model           string `json:"model"`
			Query           string `json:"query"`
			TopN            int    `json:"top_n"`
			ReturnDocuments bool   `json:"return_documents"`
			Documents       []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"documents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "cohere/rerank-v3.5" || request.Query != "evidence" || request.TopN != 2 || request.ReturnDocuments ||
			len(request.Documents) != 2 || request.Documents[0].ID != "unit_a" {
			t.Fatalf("rerank request=%+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"rerank-v3.5","results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.4}],
			"usage":{"prompt_tokens":8,"total_tokens":8},"extra_fields":{"provider":"cohere"}
		}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	outcome, err := client.Rerank(context.Background(), RerankRequest{
		Model: "cohere/rerank-v3.5", Query: "evidence",
		Candidates: []RerankCandidate{{ID: "unit_a", Text: "first"}, {ID: "unit_b", Text: "second"}}, TopN: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outcome.CandidateIDs, []string{"unit_b", "unit_a"}) || outcome.Metadata.Provider != "cohere" {
		t.Fatalf("rerank outcome=%+v", outcome)
	}
}

func TestBifrostCapabilitiesRejectMalformedProviderResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.5},{"index":0,"relevance_score":0.4}]}`))
	}))
	defer server.Close()
	client := NewBifrostClient(server.URL, server.Client(), 128)
	if _, err := client.Embed(context.Background(), EmbeddingRequest{Model: "model", Inputs: []string{"a"}, Dimensions: 3}); err == nil {
		t.Fatal("Embed accepted the wrong dimension")
	}
	if _, err := client.Rerank(context.Background(), RerankRequest{
		Model: "model", Query: "q", Candidates: []RerankCandidate{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}}, TopN: 2,
	}); err == nil {
		t.Fatal("Rerank accepted duplicate result indexes")
	}
}
