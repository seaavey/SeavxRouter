package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"seavxrouter/internal/backend"
	"testing"
)

type modelManagerStub struct{ rows map[string][]backend.Model }

func (s *modelManagerStub) ProviderModels(_ context.Context, p string) ([]backend.Model, error) {
	return s.rows[p], nil
}
func (s *modelManagerStub) AddModel(context.Context, string, string) error    { return nil }
func (s *modelManagerStub) DeleteModel(context.Context, string, string) error { return nil }
func TestProviderModelManagerPersistsAndScopesChanges(t *testing.T) {
	base := &modelManagerStub{map[string][]backend.Model{"one": {{ID: "org/model:v1"}}, "two": {{ID: "org/model:v1"}}}}
	path := filepath.Join(t.TempDir(), "models.json")
	m, e := NewProviderModelManager(base, path, func(context.Context, string, string) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = m.AddModel(context.Background(), "one", "manual/model"); e != nil {
		t.Fatal(e)
	}
	if e = m.DeleteModel(context.Background(), "one", "org/model:v1"); e != nil {
		t.Fatal(e)
	}
	m, e = NewProviderModelManager(base, path, nil)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := m.ProviderModels(context.Background(), "one", base.rows["one"])
	b, _ := m.ProviderModels(context.Background(), "two", base.rows["two"])
	if len(a) != 1 || a[0].ID != "manual/model" {
		t.Fatalf("provider one: %#v", a)
	}
	if len(b) != 1 || b[0].ID != "org/model:v1" {
		t.Fatalf("provider two: %#v", b)
	}
	if info, e := os.Stat(path); e != nil || info.Size() == 0 {
		t.Fatal("persistence missing", e)
	}
}
func TestStrictProbeRequiresValidChatResult(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{{"valid", `{"choices":[{"message":{"content":"ok"}}]}`, false}, {"malformed", `{}`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.body)) }))
			defer srv.Close()
			err := StrictProbe(context.Background(), srv.URL, "m", srv.Client())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
