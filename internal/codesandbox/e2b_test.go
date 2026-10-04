package codesandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeE2B struct {
	t          *testing.T
	mu         sync.Mutex
	created    map[string]any
	killed     []string
	uploads    map[string]string
	executions []string
	files      map[string]string
	userStream string
	createCode int
	hangUser   bool
}

func (f *fakeE2B) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "e2b_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.createCode != 0 {
			w.WriteHeader(f.createCode)
			return
		}
		f.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"templateID":"code-interpreter-v1","sandboxID":"sbx1","clientID":"c","envdVersion":"0.2.0","envdAccessToken":"envd-token"}`)
	})
	mux.HandleFunc("DELETE /sandboxes/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.killed = append(f.killed, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Access-Token") != "envd-token" || r.Header.Get("E2b-Sandbox-Id") != "sbx1" || r.Header.Get("E2b-Sandbox-Port") != "49983" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		filePath := r.URL.Query().Get("path")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			payload, _ := io.ReadAll(r.Body)
			f.uploads[filePath] = string(payload)
		case http.MethodGet:
			content, ok := f.files[filePath]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, content)
		}
	})
	mux.HandleFunc("POST /execute", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Access-Token") != "envd-token" || r.Header.Get("E2b-Sandbox-Port") != "49999" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.executions = append(f.executions, body.Code)
		index := len(f.executions)
		f.mu.Unlock()
		switch {
		case index == 1:
			_, _ = io.WriteString(w, `{"type":"end_of_execution"}`+"\n")
		case strings.Contains(body.Code, "_nano_entries"):
			names := []map[string]any{}
			for name, content := range f.files {
				names = append(names, map[string]any{"name": strings.TrimPrefix(name, e2bOutputDir+"/"), "size": len(content)})
			}
			names = append(names, map[string]any{"name": "link.csv", "size": -1}, map[string]any{"name": "../escape.csv", "size": 1})
			listing, _ := json.Marshal(names)
			stdout, _ := json.Marshal(map[string]string{"type": "stdout", "text": string(listing) + "\n"})
			_, _ = w.Write(append(stdout, '\n'))
			_, _ = io.WriteString(w, `{"type":"end_of_execution"}`+"\n")
		default:
			if f.hangUser {
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			_, _ = io.WriteString(w, f.userStream)
		}
	})
	return mux
}

func newFakeE2B(t *testing.T) (*fakeE2B, *E2BRunner) {
	t.Helper()
	fake := &fakeE2B{t: t, uploads: map[string]string{}, files: map[string]string{}}
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	runner, err := NewE2BRunner(E2BConfig{
		APIKey: "e2b_test", APIURL: server.URL, SandboxURL: server.URL,
		ExecutionTimeout: 2 * time.Second, MaxStdoutBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fake, runner
}

func TestNewE2BRunnerRequiresCredential(t *testing.T) {
	if _, err := NewE2BRunner(E2BConfig{APIKey: "  "}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err=%v", err)
	}
}

func TestE2BRunnerOmitsUnspecifiedMetadata(t *testing.T) {
	fake, runner := newFakeE2B(t)
	sandbox, err := runner.create(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.kill(context.Background(), sandbox)
	if value, exists := fake.created["metadata"]; exists && value == nil {
		t.Fatal("E2B rejects null metadata; omit it when unspecified")
	}
}

func TestE2BRunnerRunsIsolatedProgramAndCollectsOutputs(t *testing.T) {
	fake, runner := newFakeE2B(t)
	fake.files[e2bOutputDir+"/summary.csv"] = "a,b\n1,2\n"
	fake.userStream = strings.Join([]string{
		`{"type":"stdout","text":"hello \u001b[31mworld\u001b[0m and a long tail of output"}`,
		`{"type":"stderr","text":"warn"}`,
		`{"type":"result","text":"42","png":"iVBOR","is_main_result":true}`,
		`{"type":"end_of_execution"}`,
	}, "\n") + "\n"

	result, err := runner.RunPython(context.Background(), Request{
		Code:     "print('hello')",
		Files:    []File{{Path: "data/input.csv", Content: []byte("x\n1\n")}},
		Metadata: map[string]string{"run_id": "run-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.created["allow_internet_access"] != false || fake.created["templateID"] != "code-interpreter-v1" {
		t.Fatalf("create body=%v", fake.created)
	}
	if _, leaked := fake.created["envVars"]; leaked {
		t.Fatal("sandbox must not receive environment variables")
	}
	if got := fake.uploads[e2bWorkdir+"/data/input.csv"]; got != "x\n1\n" {
		t.Fatalf("uploads=%v", fake.uploads)
	}
	if len(fake.killed) != 1 || fake.killed[0] != "sbx1" {
		t.Fatalf("killed=%v", fake.killed)
	}
	if !result.StdoutTruncated || len(result.Stdout) != 32 || result.Stderr != "warn" {
		t.Fatalf("stdout=%q truncated=%v stderr=%q", result.Stdout, result.StdoutTruncated, result.Stderr)
	}
	if len(result.DisplayTexts) != 1 || result.DisplayTexts[0] != "42" || result.DisplayImages != 1 {
		t.Fatalf("display=%v images=%d", result.DisplayTexts, result.DisplayImages)
	}
	if !result.OutputsCollected || len(result.Outputs) != 1 || result.Outputs[0].Path != "summary.csv" || string(result.Outputs[0].Content) != "a,b\n1,2\n" {
		t.Fatalf("outputs=%+v", result.Outputs)
	}
	reasons := map[string]string{}
	for _, skipped := range result.SkippedOutputs {
		reasons[skipped.Name] = skipped.Reason
	}
	if reasons["link.csv"] != "not_regular_file" || reasons["../escape.csv"] != "invalid_name" {
		t.Fatalf("skipped=%v", result.SkippedOutputs)
	}
}

func TestE2BRunnerReturnsPythonErrorsWithoutANSI(t *testing.T) {
	fake, runner := newFakeE2B(t)
	fake.userStream = `{"type":"error","name":"ZeroDivisionError","value":"division by zero","traceback":"\u001b[0;31mZeroDivisionError\u001b[0m: division by zero"}` + "\n" +
		`{"type":"end_of_execution"}` + "\n"
	result, err := runner.RunPython(context.Background(), Request{Code: "1/0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := fake.created["metadata"]; present {
		t.Fatal("empty metadata must be omitted; the API rejects null")
	}
	if result.Error == nil || result.Error.Name != "ZeroDivisionError" || strings.Contains(result.Error.Traceback, "\x1b") {
		t.Fatalf("error=%+v", result.Error)
	}
}

func TestE2BRunnerReportsTimeoutAndStillKillsSandbox(t *testing.T) {
	fake, runner := newFakeE2B(t)
	runner.config.ExecutionTimeout = 100 * time.Millisecond
	fake.hangUser = true
	result, err := runner.RunPython(context.Background(), Request{Code: "while True: pass"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut || result.OutputsCollected {
		t.Fatalf("result=%+v", result)
	}
	if len(fake.killed) != 1 {
		t.Fatalf("killed=%v", fake.killed)
	}
}

func TestE2BRunnerMapsProviderFailures(t *testing.T) {
	fake, runner := newFakeE2B(t)
	fake.createCode = http.StatusTooManyRequests
	if _, err := runner.RunPython(context.Background(), Request{Code: "1"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err=%v", err)
	}
	fake.createCode = http.StatusInternalServerError
	_, err := runner.RunPython(context.Background(), Request{Code: "1"})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "e2b_test") {
		t.Fatalf("err=%v", err)
	}
}

func TestE2BRunnerRejectsUnsafeInputPathsBeforeCreatingSandbox(t *testing.T) {
	fake, runner := newFakeE2B(t)
	for _, filePath := range []string{"../etc/passwd", "/abs.csv", "output/x.csv", "Data/X.csv"} {
		_, err := runner.RunPython(context.Background(), Request{Code: "1", Files: []File{{Path: filePath, Content: []byte("x")}}})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("path=%q err=%v", filePath, err)
		}
	}
	if fake.created != nil {
		t.Fatal("sandbox must not be created for an invalid request")
	}
}
