package app_test

import (
	"context"

	"github.com/huangxinxinyu/nano-notebook/internal/models"
)

type recordingModelClient struct {
	calls   int
	request models.ChatRequest
	result  models.ChatResult
	err     error
}

func (c *recordingModelClient) Complete(_ context.Context, request models.ChatRequest) (models.ChatResult, error) {
	c.calls++
	c.request = request
	return c.result, c.err
}
