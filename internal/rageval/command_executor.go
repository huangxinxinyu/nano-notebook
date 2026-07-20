package rageval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type CommandExecutorConfig struct {
	Command        string
	Args           []string
	Env            []string
	Timeout        time.Duration
	MaxOutputBytes int64
}

type CommandExecutor struct {
	config CommandExecutorConfig
}

func NewCommandExecutor(config CommandExecutorConfig) (*CommandExecutor, error) {
	config.Command = strings.TrimSpace(config.Command)
	if config.Command == "" || config.Timeout <= 0 || config.Timeout > 30*time.Minute || config.MaxOutputBytes < 1 || config.MaxOutputBytes > 8<<20 || len(config.Args) > 32 || len(config.Env) > 64 {
		return nil, errors.New("RAG Eval command Executor configuration is invalid")
	}
	return &CommandExecutor{config: config}, nil
}

func (e *CommandExecutor) ExecuteCase(ctx context.Context, evalCase Case, config PinnedConfig) (Observation, error) {
	if e == nil {
		return Observation{}, errors.New("nil RAG Eval command Executor")
	}
	payload, err := json.Marshal(struct {
		SchemaVersion int          `json:"schema_version"`
		Case          Case         `json:"case"`
		PinnedConfig  PinnedConfig `json:"pinned_config"`
	}{SchemaVersion: 1, Case: evalCase, PinnedConfig: config})
	if err != nil {
		return Observation{}, err
	}
	caseContext, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()
	command := exec.CommandContext(caseContext, e.config.Command, e.config.Args...)
	command.Stdin = bytes.NewReader(payload)
	command.Env = append(os.Environ(), e.config.Env...)
	stdout := &boundedExecutorOutput{limit: e.config.MaxOutputBytes}
	stderr := &boundedExecutorOutput{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if errors.Is(caseContext.Err(), context.DeadlineExceeded) {
			return Observation{}, errors.New("RAG Eval product Case timed out")
		}
		return Observation{}, errors.New("RAG Eval product Case failed")
	}
	if stdout.overflow {
		return Observation{}, errors.New("RAG Eval product Observation exceeded output budget")
	}
	var observation Observation
	decoder := json.NewDecoder(bytes.NewReader(stdout.payload.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, errors.New("RAG Eval product Observation is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Observation{}, errors.New("RAG Eval product Observation has trailing content")
	}
	return observation, nil
}

type boundedExecutorOutput struct {
	limit    int64
	payload  bytes.Buffer
	overflow bool
}

func (w *boundedExecutorOutput) Write(payload []byte) (int, error) {
	written := len(payload)
	remaining := w.limit + 1 - int64(w.payload.Len())
	if remaining > 0 {
		if int64(len(payload)) > remaining {
			payload = payload[:remaining]
		}
		_, _ = w.payload.Write(payload)
	}
	if int64(w.payload.Len()) > w.limit {
		w.overflow = true
	}
	return written, nil
}
