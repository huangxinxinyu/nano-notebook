package codesandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	e2bDefaultDomain   = "e2b.app"
	e2bDefaultTemplate = "code-interpreter-v1"
	e2bEnvdPort        = 49983
	e2bJupyterPort     = 49999
	e2bUser            = "user"
	e2bWorkdir         = "/home/user/workspace"
	e2bOutputDir       = e2bWorkdir + "/output"
	// The provider reclaims a sandbox this long after its execution deadline
	// even when the explicit kill request is lost.
	e2bSandboxGrace = 2 * time.Minute
	e2bSetupTimeout = 20 * time.Second
	e2bKillTimeout  = 10 * time.Second
	// The listing holds at most 4*MaxOutputFiles short names.
	e2bManifestMaxBytes = 64 * 1024
)

var (
	e2bSandboxIDPattern   = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	e2bDomainPattern      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}\.)+[a-z]{2,63}$`)
	e2bInputPathPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}(/[a-z0-9][a-z0-9._-]{0,79}){0,3}$`)
	e2bOutputNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	ansiEscapeSequence    = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	e2bManifestCodeFormat = `import json as _nano_json, os as _nano_os
_nano_entries = []
if _nano_os.path.isdir(%[1]q):
    for _nano_name in sorted(_nano_os.listdir(%[1]q))[:%[2]d]:
        _nano_path = _nano_os.path.join(%[1]q, _nano_name)
        _nano_regular = _nano_os.path.isfile(_nano_path) and not _nano_os.path.islink(_nano_path)
        _nano_entries.append({"name": _nano_name, "size": _nano_os.path.getsize(_nano_path) if _nano_regular else -1})
print(_nano_json.dumps(_nano_entries))
`
	e2bSetupCode = fmt.Sprintf("import os\nos.makedirs(%q, exist_ok=True)\nos.chdir(%q)\n", e2bOutputDir, e2bWorkdir)
)

type E2BConfig struct {
	APIKey   string
	Template string
	Domain   string
	// APIURL overrides https://api.<Domain>.
	APIURL string
	// SandboxURL routes every in-sandbox request to one base URL; the target
	// sandbox and port travel in headers. Tests use it; production leaves it empty.
	SandboxURL         string
	HTTPClient         *http.Client
	ExecutionTimeout   time.Duration
	MaxStdoutBytes     int
	MaxStderrBytes     int
	MaxDisplayTexts    int
	MaxDisplayBytes    int
	MaxInputFiles      int
	MaxInputBytes      int64
	MaxOutputFiles     int
	MaxOutputFileBytes int64
	MaxStreamBytes     int64
}

type E2BRunner struct {
	config E2BConfig
	apiURL string
}

type e2bSandbox struct {
	ID                 string
	Domain             string
	EnvdAccessToken    string
	TrafficAccessToken string
}

func NewE2BRunner(config E2BConfig) (*E2BRunner, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, ErrNotConfigured
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	if strings.TrimSpace(config.Template) == "" {
		config.Template = e2bDefaultTemplate
	}
	if strings.TrimSpace(config.Domain) == "" {
		config.Domain = e2bDefaultDomain
	}
	if !e2bDomainPattern.MatchString(config.Domain) {
		return nil, errors.New("invalid E2B domain")
	}
	apiURL := strings.TrimRight(strings.TrimSpace(config.APIURL), "/")
	if apiURL == "" {
		apiURL = "https://api." + config.Domain
	}
	if parsed, err := url.Parse(apiURL); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.New("invalid E2B API URL")
	}
	config.SandboxURL = strings.TrimRight(strings.TrimSpace(config.SandboxURL), "/")
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	if config.ExecutionTimeout <= 0 {
		config.ExecutionTimeout = 60 * time.Second
	}
	defaultInt(&config.MaxStdoutBytes, 16*1024)
	defaultInt(&config.MaxStderrBytes, 8*1024)
	defaultInt(&config.MaxDisplayTexts, 8)
	defaultInt(&config.MaxDisplayBytes, 4*1024)
	defaultInt(&config.MaxInputFiles, 16)
	defaultInt(&config.MaxOutputFiles, 8)
	defaultInt64(&config.MaxInputBytes, 8*1024*1024)
	defaultInt64(&config.MaxOutputFileBytes, 1024*1024)
	defaultInt64(&config.MaxStreamBytes, 32*1024*1024)
	return &E2BRunner{config: config, apiURL: apiURL}, nil
}

func defaultInt(value *int, fallback int) {
	if *value <= 0 {
		*value = fallback
	}
}

func defaultInt64(value *int64, fallback int64) {
	if *value <= 0 {
		*value = fallback
	}
}

func (r *E2BRunner) RunPython(ctx context.Context, request Request) (Result, error) {
	if err := r.validateRequest(request); err != nil {
		return Result{}, err
	}
	sandbox, err := r.create(ctx, request.Metadata)
	if err != nil {
		return Result{}, err
	}
	defer r.kill(ctx, sandbox)
	for _, file := range request.Files {
		if err := r.upload(ctx, sandbox, path.Join(e2bWorkdir, file.Path), file.Content); err != nil {
			return Result{}, err
		}
	}
	setup, complete, err := r.execute(ctx, sandbox, e2bSetupCode, e2bSetupTimeout, r.config.MaxStdoutBytes)
	if err != nil {
		return Result{}, err
	}
	if !complete || setup.TimedOut || setup.Error != nil {
		return Result{}, fmt.Errorf("%w: sandbox setup failed", ErrUnavailable)
	}
	result, complete, err := r.execute(ctx, sandbox, request.Code, r.config.ExecutionTimeout, r.config.MaxStdoutBytes)
	if err != nil {
		return Result{}, err
	}
	if !complete || result.TimedOut {
		return result, nil
	}
	if err := r.collectOutputs(ctx, sandbox, &result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (r *E2BRunner) validateRequest(request Request) error {
	if strings.TrimSpace(request.Code) == "" || !utf8.ValidString(request.Code) || len(request.Files) > r.config.MaxInputFiles {
		return ErrInvalidRequest
	}
	var total int64
	seen := make(map[string]bool, len(request.Files))
	for _, file := range request.Files {
		if !e2bInputPathPattern.MatchString(file.Path) || strings.Contains(file.Path, "..") || seen[file.Path] ||
			strings.HasPrefix(file.Path, "output/") {
			return ErrInvalidRequest
		}
		seen[file.Path] = true
		total += int64(len(file.Content))
	}
	if total > r.config.MaxInputBytes {
		return ErrInvalidRequest
	}
	return nil
}

func (r *E2BRunner) create(ctx context.Context, metadata map[string]string) (e2bSandbox, error) {
	ttl := int((r.config.ExecutionTimeout + e2bSetupTimeout + e2bSandboxGrace) / time.Second)
	fields := map[string]any{"templateID": r.config.Template, "timeout": ttl, "allow_internet_access": false}
	if len(metadata) > 0 {
		// The API rejects a null metadata object.
		fields["metadata"] = metadata
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return e2bSandbox{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.apiURL+"/v2/sandboxes", bytes.NewReader(body))
	if err != nil {
		return e2bSandbox{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Key", r.config.APIKey)
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return e2bSandbox{}, transportError(ctx, "create sandbox")
	}
	defer response.Body.Close()
	if err := statusError(response, "create sandbox", http.StatusCreated, http.StatusOK); err != nil {
		return e2bSandbox{}, err
	}
	var envelope struct {
		SandboxID          string  `json:"sandboxID"`
		Domain             *string `json:"domain"`
		EnvdAccessToken    string  `json:"envdAccessToken"`
		TrafficAccessToken *string `json:"trafficAccessToken"`
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil || json.Unmarshal(payload, &envelope) != nil || !e2bSandboxIDPattern.MatchString(envelope.SandboxID) {
		return e2bSandbox{}, ErrInvalidResponse
	}
	sandbox := e2bSandbox{ID: envelope.SandboxID, Domain: r.config.Domain, EnvdAccessToken: envelope.EnvdAccessToken}
	if envelope.Domain != nil && *envelope.Domain != "" {
		if !e2bDomainPattern.MatchString(*envelope.Domain) {
			r.kill(ctx, sandbox)
			return e2bSandbox{}, ErrInvalidResponse
		}
		sandbox.Domain = *envelope.Domain
	}
	if envelope.TrafficAccessToken != nil {
		sandbox.TrafficAccessToken = *envelope.TrafficAccessToken
	}
	return sandbox, nil
}

// kill is best effort: the sandbox TTL bounds cost if the request is lost.
func (r *E2BRunner) kill(ctx context.Context, sandbox e2bSandbox) {
	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e2bKillTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(killCtx, http.MethodDelete, r.apiURL+"/sandboxes/"+url.PathEscape(sandbox.ID), nil)
	if err != nil {
		return
	}
	request.Header.Set("X-API-Key", r.config.APIKey)
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	response.Body.Close()
}

func (r *E2BRunner) sandboxRequest(ctx context.Context, method string, sandbox e2bSandbox, port int, target string, body io.Reader) (*http.Request, error) {
	base := r.config.SandboxURL
	if base == "" {
		base = fmt.Sprintf("https://%d-%s.%s", port, sandbox.ID, sandbox.Domain)
	}
	request, err := http.NewRequestWithContext(ctx, method, base+target, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("E2b-Sandbox-Id", sandbox.ID)
	request.Header.Set("E2b-Sandbox-Port", strconv.Itoa(port))
	if sandbox.EnvdAccessToken != "" {
		request.Header.Set("X-Access-Token", sandbox.EnvdAccessToken)
	}
	if sandbox.TrafficAccessToken != "" {
		request.Header.Set("E2B-Traffic-Access-Token", sandbox.TrafficAccessToken)
	}
	return request, nil
}

func fileTarget(filePath string) string {
	query := url.Values{}
	query.Set("path", filePath)
	query.Set("username", e2bUser)
	return "/files?" + query.Encode()
}

func (r *E2BRunner) upload(ctx context.Context, sandbox e2bSandbox, filePath string, content []byte) error {
	request, err := r.sandboxRequest(ctx, http.MethodPost, sandbox, e2bEnvdPort, fileTarget(filePath), bytes.NewReader(content))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return transportError(ctx, "upload file")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	return statusError(response, "upload file", http.StatusOK, http.StatusCreated, http.StatusNoContent)
}

func (r *E2BRunner) download(ctx context.Context, sandbox e2bSandbox, filePath string) ([]byte, bool, error) {
	request, err := r.sandboxRequest(ctx, http.MethodGet, sandbox, e2bEnvdPort, fileTarget(filePath), nil)
	if err != nil {
		return nil, false, err
	}
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		return nil, false, transportError(ctx, "download file")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if err := statusError(response, "download file", http.StatusOK); err != nil {
		return nil, false, err
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, r.config.MaxOutputFileBytes+1))
	if err != nil {
		return nil, false, transportError(ctx, "download file")
	}
	if int64(len(payload)) > r.config.MaxOutputFileBytes {
		return nil, false, nil
	}
	return payload, true, nil
}

type e2bMessage struct {
	Type      string  `json:"type"`
	Text      *string `json:"text"`
	PNG       *string `json:"png"`
	JPEG      *string `json:"jpeg"`
	SVG       *string `json:"svg"`
	Name      string  `json:"name"`
	Value     string  `json:"value"`
	Traceback string  `json:"traceback"`
}

// execute runs one cell. complete is false when the stream ended before the
// kernel reported the end of execution, so the kernel may still be busy.
func (r *E2BRunner) execute(ctx context.Context, sandbox e2bSandbox, code string, timeout time.Duration, maxStdout int) (Result, bool, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return Result{}, false, err
	}
	request, err := r.sandboxRequest(execCtx, http.MethodPost, sandbox, e2bJupyterPort, "/execute", bytes.NewReader(body))
	if err != nil {
		return Result{}, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.config.HTTPClient.Do(request)
	if err != nil {
		if ctx.Err() == nil && execCtx.Err() != nil {
			return Result{TimedOut: true}, false, nil
		}
		return Result{}, false, transportError(ctx, "execute code")
	}
	defer response.Body.Close()
	if err := statusError(response, "execute code", http.StatusOK); err != nil {
		return Result{}, false, err
	}
	var result Result
	var stdout, stderr strings.Builder
	decoder := json.NewDecoder(io.LimitReader(response.Body, r.config.MaxStreamBytes))
	complete := false
	for !complete {
		var message e2bMessage
		if err := decoder.Decode(&message); err != nil {
			if ctx.Err() != nil {
				return Result{}, false, ctx.Err()
			}
			if execCtx.Err() != nil {
				result.TimedOut = true
			}
			break
		}
		switch message.Type {
		case "stdout":
			if message.Text != nil {
				appendBounded(&stdout, *message.Text, maxStdout, &result.StdoutTruncated)
			}
		case "stderr":
			if message.Text != nil {
				appendBounded(&stderr, *message.Text, r.config.MaxStderrBytes, &result.StderrTruncated)
			}
		case "result":
			if message.PNG != nil || message.JPEG != nil || message.SVG != nil {
				result.DisplayImages++
			}
			if message.Text != nil && len(result.DisplayTexts) < r.config.MaxDisplayTexts {
				result.DisplayTexts = append(result.DisplayTexts, truncateHead(*message.Text, r.config.MaxDisplayBytes))
			}
		case "error":
			result.Error = &ExecutionError{
				Name:      truncateHead(message.Name, 256),
				Value:     truncateHead(stripANSI(message.Value), 1024),
				Traceback: truncateTail(stripANSI(message.Traceback), 8*1024),
			}
		case "end_of_execution":
			complete = true
		case "unexpected_end_of_execution":
			if result.Error == nil {
				result.Error = &ExecutionError{Name: "KernelDied", Value: "the Python kernel stopped before the code finished"}
			}
			complete = true
		}
	}
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	if !complete && !result.TimedOut && result.Error == nil {
		result.Error = &ExecutionError{Name: "OutputLimitExceeded", Value: "the execution output stream ended early or exceeded its size limit"}
	}
	return result, complete, nil
}

func (r *E2BRunner) collectOutputs(ctx context.Context, sandbox e2bSandbox, result *Result) error {
	manifest, complete, err := r.execute(ctx, sandbox, fmt.Sprintf(e2bManifestCodeFormat, e2bOutputDir, 4*r.config.MaxOutputFiles), e2bSetupTimeout, e2bManifestMaxBytes)
	if err != nil {
		return err
	}
	var entries []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if !complete || manifest.Error != nil || manifest.StdoutTruncated ||
		json.Unmarshal([]byte(strings.TrimSpace(manifest.Stdout)), &entries) != nil {
		// The program can overwrite the interpreter state the manifest uses.
		result.SkippedOutputs = append(result.SkippedOutputs, SkippedOutput{Name: "output/", Reason: "output_listing_failed"})
		return nil
	}
	result.OutputsCollected = true
	for _, entry := range entries {
		name := truncateHead(entry.Name, 128)
		switch {
		case !e2bOutputNamePattern.MatchString(entry.Name):
			result.SkippedOutputs = append(result.SkippedOutputs, SkippedOutput{Name: name, Reason: "invalid_name"})
		case entry.Size < 0:
			result.SkippedOutputs = append(result.SkippedOutputs, SkippedOutput{Name: name, Reason: "not_regular_file"})
		case entry.Size > r.config.MaxOutputFileBytes:
			result.SkippedOutputs = append(result.SkippedOutputs, SkippedOutput{Name: name, Reason: "too_large"})
		case len(result.Outputs) >= r.config.MaxOutputFiles:
			result.SkippedOutputs = append(result.SkippedOutputs, SkippedOutput{Name: name, Reason: "too_many_files"})
		default:
			content, ok, err := r.download(ctx, sandbox, path.Join(e2bOutputDir, entry.Name))
			if err != nil {
				return err
			}
			if !ok {
				result.SkippedOutputs = append(result.SkippedOutputs, SkippedOutput{Name: name, Reason: "unreadable"})
				continue
			}
			result.Outputs = append(result.Outputs, File{Path: entry.Name, Content: content})
		}
	}
	return nil
}

func transportError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s request failed", ErrUnavailable, operation)
}

func statusError(response *http.Response, operation string, accepted ...int) error {
	for _, status := range accepted {
		if response.StatusCode == status {
			return nil
		}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %s", ErrRateLimited, operation)
	}
	return fmt.Errorf("%w: %s returned HTTP %d", ErrUnavailable, operation, response.StatusCode)
}

func appendBounded(builder *strings.Builder, text string, maximum int, truncated *bool) {
	remaining := maximum - builder.Len()
	if remaining <= 0 {
		if text != "" {
			*truncated = true
		}
		return
	}
	if len(text) > remaining {
		builder.WriteString(truncateHead(text, remaining))
		*truncated = true
		return
	}
	builder.WriteString(text)
}

func stripANSI(value string) string {
	return ansiEscapeSequence.ReplaceAllString(value, "")
}

func truncateHead(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	cut := maximum
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

func truncateTail(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	cut := len(value) - maximum
	for cut < len(value) && !utf8.RuneStart(value[cut]) {
		cut++
	}
	return value[cut:]
}
