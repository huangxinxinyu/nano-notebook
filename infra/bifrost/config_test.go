package bifrost_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type bifrostConfig struct {
	Providers map[string]struct {
		Keys []struct {
			Value  string   `json:"value"`
			Models []string `json:"models"`
		} `json:"keys"`
		NetworkConfig struct {
			BaseURL string `json:"base_url"`
		} `json:"network_config"`
	} `json:"providers"`
}

func loadConfig(t *testing.T, path string) bifrostConfig {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config bifrostConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return config
}

func TestConfigRoutesOnlyGeminiEmbeddingModelThroughEnvironmentCredential(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"config.json", "config.prod.json"} {
		config := loadConfig(t, path)
		gemini, ok := config.Providers["gemini"]
		if !ok || len(gemini.Keys) != 1 {
			t.Fatalf("%s gemini provider=%+v configured=%v", path, gemini, ok)
		}
		key := gemini.Keys[0]
		if key.Value != "env.GEMINI_API_KEY" || !reflect.DeepEqual(key.Models, []string{"gemini-embedding-2"}) {
			t.Fatalf("%s gemini key config=%+v", path, key)
		}
		if _, ok := config.Providers["aliyun"]; !ok {
			t.Fatalf("%s Gemini embedding config removed the Aliyun generation provider", path)
		}
	}
}

// Both configs are committed, so every credential must stay an env reference.
func TestCommittedConfigsReadCredentialsOnlyFromEnvironment(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"config.json", "config.prod.json"} {
		for name, provider := range loadConfig(t, path).Providers {
			for _, key := range provider.Keys {
				if !strings.HasPrefix(key.Value, "env.") {
					t.Fatalf("%s provider %s has a literal credential; use env.<NAME>", path, name)
				}
			}
		}
	}
}

func TestProductionConfigDiffersOnlyByRegionalEndpoint(t *testing.T) {
	t.Parallel()

	local, production := loadConfig(t, "config.json"), loadConfig(t, "config.prod.json")
	if got := local.Providers["aliyun"].NetworkConfig.BaseURL; got != "https://dashscope.aliyuncs.com/compatible-mode" {
		t.Fatalf("local DashScope endpoint=%q", got)
	}
	if got := production.Providers["aliyun"].NetworkConfig.BaseURL; got != "https://dashscope-intl.aliyuncs.com/compatible-mode" {
		t.Fatalf("production DashScope endpoint=%q; the mainland endpoint hangs from Singapore", got)
	}
	if len(local.Providers) != len(production.Providers) {
		t.Fatalf("providers local=%d production=%d", len(local.Providers), len(production.Providers))
	}
	for name, provider := range local.Providers {
		other, ok := production.Providers[name]
		if !ok || len(provider.Keys) != len(other.Keys) {
			t.Fatalf("production provider %s missing or has different keys", name)
		}
		for index := range provider.Keys {
			if provider.Keys[index].Value != other.Keys[index].Value || !reflect.DeepEqual(provider.Keys[index].Models, other.Keys[index].Models) {
				t.Fatalf("provider %s key %d differs between environments", name, index)
			}
		}
	}
}
