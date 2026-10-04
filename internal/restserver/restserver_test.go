package restserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const name = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func do(t *testing.T, s *Server, method, path, body, pw string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if pw != "" {
		req.SetBasicAuth("restic", pw)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestAuthRequired(t *testing.T) {
	s := New(t.TempDir())
	if w := do(t, s, "POST", "/r1/?create=true", "", "x"); w.Code != 401 {
		t.Fatalf("no token provisioned must refuse everything, got %d", w.Code)
	}
	s.SetToken("secret")
	if w := do(t, s, "POST", "/r1/?create=true", "", "wrong"); w.Code != 401 {
		t.Fatalf("wrong password: %d", w.Code)
	}
}

func TestRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	s.SetToken("secret")
	if w := do(t, s, "POST", "/r1/?create=true", "", "secret"); w.Code != 200 {
		t.Fatalf("create: %d", w.Code)
	}
	if w := do(t, s, "POST", "/r1/config", "cfg", "secret"); w.Code != 200 {
		t.Fatalf("config post: %d", w.Code)
	}
	if w := do(t, s, "POST", "/r1/?create=true", "", "secret"); w.Code != http.StatusConflict {
		t.Fatalf("re-create should conflict: %d", w.Code)
	}
	if w := do(t, s, "POST", "/r1/data/"+name, "hello", "secret"); w.Code != 200 {
		t.Fatalf("post: %d", w.Code)
	}
	if w := do(t, s, "POST", "/r1/data/"+name, "again", "secret"); w.Code != 403 {
		t.Fatalf("blobs are write-once: %d", w.Code)
	}
	if w := do(t, s, "GET", "/r1/data/"+name, "", "secret"); w.Body.String() != "hello" {
		t.Fatalf("get: %q", w.Body.String())
	}
	if w := do(t, s, "GET", "/r1/data/", "", "secret"); !strings.Contains(w.Body.String(), name) {
		t.Fatalf("list: %s", w.Body.String())
	}
	if w := do(t, s, "DELETE", "/r1/data/"+name, "", "secret"); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(t, s, "DELETE", "/r1/config", "", "secret"); w.Code != 403 {
		t.Fatalf("config must not be deletable: %d", w.Code)
	}
	if w := do(t, s, "DELETE", "/r1/", "", "secret"); w.Code != 403 {
		t.Fatalf("repo must not be deletable: %d", w.Code)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	s := New(t.TempDir())
	s.SetToken("secret")
	for _, p := range []string{"/../etc/passwd", "/r1/data/../../x", "/r1/data/zz", "/..%2f/x", "/r1/keys/" + strings.Repeat("a", 64) + "/x"} {
		if w := do(t, s, "GET", p, "", "secret"); w.Code == 200 {
			t.Errorf("%s unexpectedly succeeded", p)
		}
	}
	if w := do(t, s, "POST", "/r1/data/../../evil", "x", "secret"); w.Code == 200 {
		t.Error("traversal write succeeded")
	}
}
