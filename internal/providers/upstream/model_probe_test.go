package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"seavxrouter/internal/auth"
	"testing"
)

func TestProbeChatRejectsEmptySuccessfulResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer s.Close()
	e, err := NewEngine("test", s.URL, auth.AuthStore{}, []*auth.Auth{{AccessToken: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ProbeChat(context.Background(), "manual"); err == nil {
		t.Fatal("empty HTTP 200 must not verify a model")
	}
}
