package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// registry refuses every post until the agent registers, the way the real one
// refuses an agent id this credential never registered.
func registry(t *testing.T) (*httptest.Server, *int, *int) {
	t.Helper()
	registered, posts := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents/register":
			registered++
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		case "/v1/rooms/lobby/posts":
			posts++
			if registered == 0 {
				http.Error(w, "register this autonomous agent under the presented delegation first: \"a\" has never been registered by you", http.StatusForbidden)
				return
			}
			w.Write([]byte(`{"ok":true}`))
		default:
			http.Error(w, "nope", http.StatusForbidden)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &registered, &posts
}

func TestUnregisteredRefusalRegistersAndRetriesOnce(t *testing.T) {
	srv, registered, posts := registry(t)
	hooks := 0
	h := &HTTPStore{Base: srv.URL, OnUnregistered: func(plain *HTTPStore) error {
		hooks++
		if plain.OnUnregistered != nil {
			t.Error("the hook got a store that would call it again")
		}
		return plain.JSON(http.MethodPost, "/v1/agents/register", map[string]string{"agent_id": "a"}, nil)
	}}
	var out struct{ OK bool }
	if err := h.JSON(http.MethodPost, "/v1/rooms/lobby/posts", map[string]string{"body": "hi"}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || hooks != 1 || *registered != 1 || *posts != 2 {
		t.Errorf("ok=%t hooks=%d registered=%d posts=%d; want true, 1, 1, 2", out.OK, hooks, *registered, *posts)
	}
}

func TestUnregisteredRefusalIsTypedWithoutAHook(t *testing.T) {
	srv, _, _ := registry(t)
	err := (&HTTPStore{Base: srv.URL}).JSON(http.MethodPost, "/v1/rooms/lobby/posts", map[string]string{}, nil)
	var unregistered *UnregisteredError
	if !errors.As(err, &unregistered) {
		t.Fatalf("err = %v, want an UnregisteredError", err)
	}
	// Any other 403 is not a registration problem, and must not trigger one.
	hooks := 0
	h := &HTTPStore{Base: srv.URL, OnUnregistered: func(*HTTPStore) error { hooks++; return nil }}
	if err := h.JSON(http.MethodGet, "/elsewhere", nil, nil); err == nil || errors.As(err, &unregistered) || hooks != 0 {
		t.Errorf("unrelated 403: err=%v hooks=%d", err, hooks)
	}
}

// A registration that fails reports the original refusal, which says what to do.
func TestFailedRegistrationKeepsTheOriginalRefusal(t *testing.T) {
	srv, _, posts := registry(t)
	h := &HTTPStore{Base: srv.URL, OnUnregistered: func(*HTTPStore) error { return errors.New("boom") }}
	err := h.JSON(http.MethodPost, "/v1/rooms/lobby/posts", map[string]string{}, nil)
	var unregistered *UnregisteredError
	if !errors.As(err, &unregistered) || *posts != 1 {
		t.Errorf("err=%v posts=%d; want the refusal and no retry", err, *posts)
	}
}
