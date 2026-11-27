package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"seavxrouter/internal/backend"
	"strings"
	"testing"
)

type rejectingModelManager struct{ modelManagerStub }

func (*rejectingModelManager) AddModel(context.Context, string, string) error {
	return fmt.Errorf("probe: %w", &backend.HTTPError{Status: 401, Message: "upstream rejected credential"})
}

func TestModelManagementResponseShapeAndUpstreamStatus(t *testing.T) {
	s := &Server{modelManager: &rejectingModelManager{modelManagerStub{rows: map[string][]backend.Model{"test": {{ID: "org/model"}}}}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("provider", "test")
	w := httptest.NewRecorder()
	s.handleProviderModels(w, r)
	if !strings.Contains(w.Body.String(), `"id":"org/model"`) {
		t.Fatalf("wrong JSON shape: %s", w.Body)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"model":"bad"}`))
	r.SetPathValue("provider", "test")
	w = httptest.NewRecorder()
	s.handleAddProviderModel(w, r)
	if w.Code == 401 || !strings.Contains(w.Body.String(), `"upstream_status":401`) {
		t.Fatalf("upstream auth must not expire dashboard session: %d %s", w.Code, w.Body)
	}
}
