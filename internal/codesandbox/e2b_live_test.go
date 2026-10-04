//go:build live

package codesandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestE2BRunnerLive(t *testing.T) {
	if os.Getenv("NANO_E2B_LIVE") != "1" {
		t.Skip("set NANO_E2B_LIVE=1 to run the credentialed smoke test")
	}
	apiKey := os.Getenv("NANO_E2B_API_KEY")
	if apiKey == "" {
		t.Skip("NANO_E2B_API_KEY is unavailable")
	}
	runner, err := NewE2BRunner(E2BConfig{APIKey: apiKey, ExecutionTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("configure E2B runner: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	code := `import pandas as pd, urllib.request
frame = pd.read_csv("data/prices.csv")
frame["growth"] = frame["price"].pct_change()
frame.to_csv("output/growth.csv", index=False)
print(round(frame["growth"].iloc[-1], 2))
try:
    urllib.request.urlopen("https://example.com", timeout=5)
    print("network=open")
except Exception:
    print("network=blocked")
frame["price"].sum()`
	result, err := runner.RunPython(ctx, Request{
		Code:  code,
		Files: []File{{Path: "data/prices.csv", Content: []byte("year,price\n2025,10\n2026,12\n")}},
	})
	if err != nil {
		t.Fatalf("live run failed: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("python error: %+v", result.Error)
	}
	if !strings.Contains(result.Stdout, "0.2") || !strings.Contains(result.Stdout, "network=blocked") {
		t.Fatalf("stdout=%q", result.Stdout)
	}
	if len(result.DisplayTexts) == 0 || !strings.Contains(result.DisplayTexts[0], "22") {
		t.Fatalf("display=%v", result.DisplayTexts)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].Path != "growth.csv" || !strings.Contains(string(result.Outputs[0].Content), "growth") {
		t.Fatalf("outputs=%+v skipped=%+v", result.Outputs, result.SkippedOutputs)
	}
}
