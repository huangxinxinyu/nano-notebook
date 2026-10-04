// Package codesandbox runs model-written code outside the Worker trust boundary.
// A Runner receives only the code and explicit input files; it never receives
// Nano credentials, and its returned bytes are untrusted until the caller
// validates them.
package codesandbox

import (
	"context"
	"errors"
)

var (
	ErrNotConfigured   = errors.New("Code Sandbox is not configured")
	ErrInvalidRequest  = errors.New("Code Sandbox request is invalid")
	ErrRateLimited     = errors.New("Code Sandbox Provider rate limited the request")
	ErrUnavailable     = errors.New("Code Sandbox Provider is unavailable")
	ErrInvalidResponse = errors.New("Code Sandbox Provider returned an invalid response")
)

// Runner executes one Python program in a fresh, network-isolated sandbox and
// destroys it before returning.
type Runner interface {
	RunPython(context.Context, Request) (Result, error)
}

type Request struct {
	Code string
	// Files are written below the working directory at their relative paths.
	Files []File
	// Metadata labels the sandbox at the provider for cost attribution only.
	Metadata map[string]string
}

type File struct {
	Path    string
	Content []byte
}

type Result struct {
	Stdout          string
	StdoutTruncated bool
	Stderr          string
	StderrTruncated bool
	// DisplayTexts holds text/plain renderings of Jupyter display results,
	// including the value of the final expression.
	DisplayTexts     []string
	DisplayImages    int
	Error            *ExecutionError
	TimedOut         bool
	Outputs          []File
	SkippedOutputs   []SkippedOutput
	OutputsCollected bool
}

type ExecutionError struct {
	Name      string
	Value     string
	Traceback string
}

type SkippedOutput struct {
	Name   string
	Reason string
}
