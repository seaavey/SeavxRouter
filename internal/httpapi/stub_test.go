package httpapi

import (
	"context"

	"seavxrouter/internal/backend"
)

// chatStub is a BackendAPI test double shared by the httpapi tests. It records
// the request it was given and replays queued deltas.
type chatStub struct {
	gotReq     *backend.ChatRequest
	result     *backend.ChatResult
	deltas     []backend.ChatDelta
	hasNoModel string
	err        error
}

func (c *chatStub) Models(context.Context) ([]backend.Model, error) {
	return []backend.Model{{ID: "m"}}, nil
}

func (c *chatStub) HasModel(_ context.Context, id string) bool {
	return id != c.hasNoModel
}

func (c *chatStub) Complete(context.Context, string, string) (*backend.Result, error) {
	return &backend.Result{Status: "SUCCESS"}, nil
}

func (c *chatStub) Stream(context.Context, string, string, func(string)) (*backend.Result, error) {
	return &backend.Result{Status: "SUCCESS"}, nil
}

func (c *chatStub) Chat(_ context.Context, req *backend.ChatRequest, onDelta func(backend.ChatDelta)) (*backend.ChatResult, error) {
	c.gotReq = req
	if onDelta != nil {
		for _, d := range c.deltas {
			onDelta(d)
		}
	}
	return c.result, c.err
}

func (c *chatStub) Default() string { return "m" }
