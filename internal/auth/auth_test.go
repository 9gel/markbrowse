package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

var content = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte("secret notes"))
})

type client struct {
	t      *testing.T
	h      http.Handler
	cookie string
}

func (c *client) do(method, target string, form url.Values) *http.Response {
	c.t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.cookie != "" {
		r.Header.Set("Cookie", cookieName+"="+c.cookie)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	res := w.Result()
	for _, ck := range res.Cookies() {
		if ck.Name == cookieName {
			c.cookie = ck.Value
		}
	}
	return res
}

func open(t *testing.T, path string) *client {
	t.Helper()
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, h: a.Wrap(content)}
}

func wantRedirect(t *testing.T, res *http.Response, prefix string) {
	t.Helper()
	if loc := res.Header.Get("Location"); res.StatusCode != 303 ||
		!strings.HasPrefix(loc, prefix) {
		t.Fatalf("got %d %q, want redirect to %s", res.StatusCode, loc, prefix)
	}
}

func TestFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "auth.json")
	admin := open(t, path)

	// Before setup everything goes to the setup page.
	wantRedirect(t, admin.do("GET", "/notes.md", nil), SetupPath)

	// Too-short password is refused.
	res := admin.do("POST", SetupPath,
		url.Values{"username": {"boss"}, "password": {"short"}})
	if res.StatusCode != 400 {
		t.Fatalf("short password: %d", res.StatusCode)
	}

	wantRedirect(t, admin.do("POST", SetupPath,
		url.Values{"username": {"boss"}, "password": {"hunter22"}}), "/")
	if res := admin.do("GET", "/notes.md", nil); res.StatusCode != 200 {
		t.Fatalf("admin read: %d", res.StatusCode)
	}

	// Setup cannot be repeated to take over the server.
	intruder := open(t, path)
	wantRedirect(t, intruder.do("POST", SetupPath,
		url.Values{"username": {"evil"}, "password": {"hunter22"}}),
		LoginPath)
	wantRedirect(t, intruder.do("GET", "/notes.md", nil),
		LoginPath+"?next=%2Fnotes.md")

	// Admin adds a reader.
	admin.do("POST", UsersPath, url.Values{"action": {"put"},
		"username": {"amy"}, "password": {"password1"}})

	// The state survives a restart: a fresh Auth on the same file.
	amy := open(t, path)
	res = amy.do("POST", LoginPath, url.Values{"username": {"amy"},
		"password": {"wrong-one"}})
	if res.StatusCode != 400 {
		t.Fatalf("bad password: %d", res.StatusCode)
	}
	wantRedirect(t, amy.do("POST", LoginPath, url.Values{"username": {"amy"},
		"password": {"password1"}, "next": {"/a.md"}}), "/a.md")
	if res := amy.do("GET", "/a.md", nil); res.StatusCode != 200 {
		t.Fatalf("amy read: %d", res.StatusCode)
	}

	// Non-admins cannot manage users.
	res = amy.do("POST", UsersPath, url.Values{"action": {"put"},
		"username": {"x"}, "password": {"password1"}})
	if res.StatusCode != 400 {
		t.Fatalf("non-admin put: %d", res.StatusCode)
	}

	// Resetting amy's password kills her session.
	admin.do("POST", UsersPath, url.Values{"action": {"put"},
		"username": {"amy"}, "password": {"password2"}})
	// Same in-memory instance as admin's, so share it.
	amy.h = admin.h
	wantRedirect(t, amy.do("GET", "/a.md", nil), LoginPath)

	// Deleting a user is refused for yourself, allowed for others.
	res = admin.do("POST", UsersPath, url.Values{"action": {"delete"},
		"username": {"boss"}})
	if res.StatusCode != 400 {
		t.Fatalf("self delete: %d", res.StatusCode)
	}
	admin.do("POST", UsersPath, url.Values{"action": {"delete"},
		"username": {"amy"}})
	res = amy.do("POST", LoginPath, url.Values{"username": {"amy"},
		"password": {"password2"}})
	if res.StatusCode != 400 {
		t.Fatalf("deleted user login: %d", res.StatusCode)
	}

	// Logout clears the cookie.
	admin.do("GET", LogoutPath, nil)
	wantRedirect(t, admin.do("GET", "/", nil), LoginPath)
}

func TestForgedCookie(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "auth.json"))
	c.do("POST", SetupPath,
		url.Values{"username": {"boss"}, "password": {"hunter22"}})
	parts := strings.Split(c.cookie, ".")
	c.cookie = parts[0] + ".99999999999." + parts[2]
	wantRedirect(t, c.do("GET", "/", nil), LoginPath)
}

func TestCrossSitePostRejected(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "auth.json"))
	r := httptest.NewRequest("POST", SetupPath, strings.NewReader(
		"username=evil&password=hunter22"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site setup: %d", w.Code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b.md":          "/a/b.md",
		"//evil.com":       "/",
		"/\\evil.com":      "/",
		"https://evil.com": "/",
		"":                 "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
