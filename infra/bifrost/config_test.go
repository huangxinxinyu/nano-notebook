package bifrost_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestConfigRoutesOnlyGeminiEmbeddingModelThroughEnvironmentCredential(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Providers map[string]struct {
			Keys []struct {
				Value  string   `json:"value"`
				Models []string `json:"models"`
			} `json:"keys"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	gemini, ok := config.Providers["gemini"]
	if !ok || len(gemini.Keys) != 1 {
		t.Fatalf("gemini provider=%+v configured=%v", gemini, ok)
	}
	key := gemini.Keys[0]
	if key.Value != "env.GEMINI_API_KEY" || !reflect.DeepEqual(key.Models, []string{"gemini-embedding-2"}) {
		t.Fatalf("gemini key config=%+v", key)
	}
	if _, ok := config.Providers["aliyun"]; !ok {
		t.Fatal("Gemini embedding config removed the Aliyun generation provider")
	}
}
