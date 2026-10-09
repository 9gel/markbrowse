// Package auth puts password logins in front of an http.Handler.
//
// Users live in one JSON file. While it holds no users, every request is sent
// to a setup page that creates the first (admin) account. Admins add, reset
// and delete users on /__mdview/users; everyone can change their own
// password there.
//
// Sessions are stateless signed cookies. The MAC covers the user's password
// hash, so changing or resetting a password logs that user out everywhere.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName  = "mb_session"
	sessionTTL  = 30 * 24 * time.Hour
	minPassword = 8

	SetupPath  = "/__mdview/setup"
	LoginPath  = "/__mdview/login"
	LogoutPath = "/__mdview/logout"
	UsersPath  = "/__mdview/users"
)

var validName = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

type user struct {
	Hash  []byte `json:"hash"`
	Admin bool   `json:"admin"`
}

type state struct {
	Secret []byte          `json:"secret"`
	Users  map[string]user `json:"users"`
}

// Auth is the login layer. Create it with Open.
type Auth struct {
	path string
	mu   sync.RWMutex
	st   state
	// dummy is compared against for unknown users so a login attempt takes
	// the same time whether or not the name exists.
	dummy []byte
}

// Open loads the user file at path, or starts empty (setup mode) when the
// file does not exist yet.
func Open(path string) (*Auth, error) {
	a := &Auth{path: path, st: state{Users: map[string]user{}}}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-chosen path
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("reading auth file: %w", err)
	default:
		if err := json.Unmarshal(data, &a.st); err != nil {
			return nil, fmt.Errorf("parsing auth file %s: %w", path, err)
		}
		if a.st.Users == nil {
			a.st.Users = map[string]user{}
		}
	}
	if len(a.st.Secret) == 0 {
		a.st.Secret = make([]byte, 32)
		if _, err := rand.Read(a.st.Secret); err != nil {
			return nil, err
		}
	}
	a.dummy, _ = bcrypt.GenerateFromPassword([]byte("x"), bcrypt.DefaultCost)
	return a, nil
}

// save writes the state atomically. Callers hold a.mu.
func (a *Auth) save() error {
	data, err := json.MarshalIndent(a.st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(a.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.path)
}

func (a *Auth) needsSetup() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.st.Users) == 0
}

func (a *Auth) mac(name string, exp int64, hash []byte) string {
	m := hmac.New(sha256.New, a.st.Secret)
	fmt.Fprintf(m, "%s\x00%d\x00%s", name, exp, hash)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *Auth) cookieValue(name string, hash []byte) (string, time.Time) {
	exp := time.Now().Add(sessionTTL)
	n := base64.RawURLEncoding.EncodeToString([]byte(name))
	return n + "." + strconv.FormatInt(exp.Unix(), 10) + "." +
		a.mac(name, exp.Unix(), hash), exp
}

// sessionUser returns the logged-in user name, or "" when the request has no
// valid session.
func (a *Auth) sessionUser(r *http.Request) (string, user) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", user{}
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", user{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", user{}
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", user{}
	}
	name := string(raw)
	a.mu.RLock()
	defer a.mu.RUnlock()
	u, ok := a.st.Users[name]
	if !ok || !hmac.Equal([]byte(parts[2]), []byte(a.mac(name, exp, u.Hash))) {
		return "", user{}
	}
	return name, u
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (a *Auth) setSession(w http.ResponseWriter, r *http.Request,
	name string, hash []byte) {
	a.mu.RLock()
	v, exp := a.cookieValue(name, hash)
	a.mu.RUnlock()
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: v, Path: "/", Expires: exp,
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode,
	})
}

// Wrap guards next. Login, setup and user-management pages are served here;
// everything else needs a valid session.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	pages := http.NewServeMux()
	pages.HandleFunc(SetupPath, a.setup)
	pages.HandleFunc(LoginPath, a.login)
	pages.HandleFunc(LogoutPath, a.logout)
	pages.HandleFunc(UsersPath, a.users)
	// Rejects cross-site POSTs (CSRF) using Sec-Fetch-Site / Origin.
	guarded := http.NewCrossOriginProtection().Handler(pages)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case SetupPath, LoginPath, LogoutPath, UsersPath:
			guarded.ServeHTTP(w, r)
			return
		}
		if a.needsSetup() {
			http.Redirect(w, r, SetupPath, http.StatusSeeOther)
			return
		}
		if name, _ := a.sessionUser(r); name == "" {
			http.Redirect(w, r, LoginPath+"?next="+
				url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safeNext keeps post-login redirects on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func checkNew(name, pw string) string {
	if !validName.MatchString(name) {
		return "Username: 1-64 of letters, digits, . _ @ -"
	}
	if len(pw) < minPassword {
		return fmt.Sprintf("Password must be at least %d characters",
			minPassword)
	}
	return ""
}

func (a *Auth) setup(w http.ResponseWriter, r *http.Request) {
	if !a.needsSetup() {
		http.Redirect(w, r, LoginPath, http.StatusSeeOther)
		return
	}
	v := view{Title: "Set up markbrowse", Setup: true}
	if r.Method == http.MethodPost {
		name, pw := r.FormValue("username"), r.FormValue("password")
		if v.Error = checkNew(name, pw); v.Error == "" {
			hash, err := a.put(name, pw, true, true)
			if err != nil {
				v.Error = err.Error()
			} else {
				a.setSession(w, r, name, hash)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
	}
	render(w, v)
}

// put creates or replaces a user and saves. onlyIfEmpty makes it a no-op
// error when users already exist (closes the setup race).
func (a *Auth) put(name, pw string, admin, onlyIfEmpty bool) ([]byte, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if onlyIfEmpty && len(a.st.Users) > 0 {
		return nil, errors.New("setup already done")
	}
	prev, existed := a.st.Users[name]
	a.st.Users[name] = user{Hash: hash, Admin: admin}
	if err := a.save(); err != nil {
		if existed {
			a.st.Users[name] = prev
		} else {
			delete(a.st.Users, name)
		}
		log.Printf("auth: saving %s: %v", a.path, err)
		return nil, errors.New("could not save user file")
	}
	return hash, nil
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	if a.needsSetup() {
		http.Redirect(w, r, SetupPath, http.StatusSeeOther)
		return
	}
	v := view{Title: "Log in", Next: safeNext(r.FormValue("next"))}
	if r.Method == http.MethodPost {
		name, pw := r.FormValue("username"), r.FormValue("password")
		a.mu.RLock()
		u, ok := a.st.Users[name]
		a.mu.RUnlock()
		hash := a.dummy
		if ok {
			hash = u.Hash
		}
		if bcrypt.CompareHashAndPassword(hash, []byte(pw)) == nil && ok {
			a.setSession(w, r, name, u.Hash)
			http.Redirect(w, r, v.Next, http.StatusSeeOther)
			return
		}
		v.Error = "Wrong username or password"
	}
	render(w, v)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, LoginPath, http.StatusSeeOther)
}

func (a *Auth) users(w http.ResponseWriter, r *http.Request) {
	me, u := a.sessionUser(r)
	if me == "" {
		http.Redirect(w, r, LoginPath+"?next="+UsersPath, http.StatusSeeOther)
		return
	}
	v := view{Title: "Users", Me: me, Admin: u.Admin}
	if r.Method == http.MethodPost {
		v.Error, v.Notice = a.userAction(w, r, me, u)
	}
	if v.Admin {
		a.mu.RLock()
		for n, u := range a.st.Users {
			v.Users = append(v.Users, listed{n, u.Admin})
		}
		a.mu.RUnlock()
		sort.Slice(v.Users, func(i, j int) bool {
			return v.Users[i].Name < v.Users[j].Name
		})
	}
	render(w, v)
}

// userAction applies one POSTed form and returns (error, notice).
func (a *Auth) userAction(w http.ResponseWriter, r *http.Request,
	me string, u user) (string, string) {
	name, pw := r.FormValue("username"), r.FormValue("password")
	switch r.FormValue("action") {
	case "passwd":
		if bcrypt.CompareHashAndPassword(u.Hash,
			[]byte(r.FormValue("current"))) != nil {
			return "Current password is wrong", ""
		}
		if e := checkNew(me, pw); e != "" {
			return e, ""
		}
		hash, err := a.put(me, pw, u.Admin, false)
		if err != nil {
			return err.Error(), ""
		}
		a.setSession(w, r, me, hash)
		return "", "Password changed"
	case "put":
		if !u.Admin {
			return "Admins only", ""
		}
		if name == me {
			return "Use “Change my password” for your own account", ""
		}
		if e := checkNew(name, pw); e != "" {
			return e, ""
		}
		if _, err := a.put(name, pw, r.FormValue("admin") == "on",
			false); err != nil {
			return err.Error(), ""
		}
		return "", "Saved " + name
	case "delete":
		if !u.Admin {
			return "Admins only", ""
		}
		if name == me {
			return "You cannot delete yourself", ""
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		prev, ok := a.st.Users[name]
		if !ok {
			return "No such user", ""
		}
		delete(a.st.Users, name)
		if err := a.save(); err != nil {
			a.st.Users[name] = prev
			log.Printf("auth: saving %s: %v", a.path, err)
			return "Could not save user file", ""
		}
		return "", "Deleted " + name
	}
	return "Unknown action", ""
}

type listed struct {
	Name  string
	Admin bool
}

type view struct {
	Title, Error, Notice, Next, Me string
	Setup, Admin                   bool
	Users                          []listed
}

func render(w http.ResponseWriter, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; "+
		"style-src 'unsafe-inline'; form-action 'self'; "+
		"base-uri 'none'; frame-ancestors 'none'")
	if v.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	if err := pageTmpl.Execute(w, v); err != nil {
		log.Printf("auth template: %v", err)
	}
}

var pageTmpl = template.Must(template.New("auth").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
:root{color-scheme:light dark}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
max-width:420px;margin:48px auto;padding:0 16px;line-height:1.5}
form{display:grid;gap:8px;margin:0 0 24px}
input,button{font:inherit;padding:8px}
.err{color:#d1242f}.ok{color:#1a7f37}
table{width:100%;border-collapse:collapse;margin-bottom:24px}
td{padding:4px 0}td form{margin:0}
label.cb{display:flex;gap:8px;align-items:center}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Notice}}<p class="ok">{{.Notice}}</p>{{end}}
{{if .Me}}
<p>Signed in as <b>{{.Me}}</b> · <a href="/">Files</a> ·
<a href="/__mdview/logout">Log out</a></p>
{{if .Admin}}
<h2>Users</h2>
<table>{{range .Users}}<tr><td>{{.Name}}{{if .Admin}} (admin){{end}}</td>
<td>{{if ne .Name $.Me}}<form method="post">
<input type="hidden" name="action" value="delete">
<input type="hidden" name="username" value="{{.Name}}">
<button>Delete</button></form>{{end}}</td></tr>{{end}}</table>
<h2>Add user or reset password</h2>
<form method="post"><input type="hidden" name="action" value="put">
<input name="username" placeholder="Username" required autocomplete="off">
<input name="password" type="password" placeholder="Password" required
autocomplete="new-password">
<label class="cb"><input type="checkbox" name="admin"> Admin</label>
<button>Save user</button></form>
{{end}}
<h2>Change my password</h2>
<form method="post"><input type="hidden" name="action" value="passwd">
<input name="current" type="password" placeholder="Current password"
required autocomplete="current-password">
<input name="password" type="password" placeholder="New password" required
autocomplete="new-password">
<button>Change password</button></form>
{{else}}
{{if .Setup}}<p>No users yet. Create the admin account.</p>{{end}}
<form method="post">
{{if .Next}}<input type="hidden" name="next" value="{{.Next}}">{{end}}
<input name="username" placeholder="Username" required autofocus
autocomplete="username">
<input name="password" type="password" placeholder="Password" required
autocomplete="{{if .Setup}}new-password{{else}}current-password{{end}}">
<button>{{if .Setup}}Create admin{{else}}Log in{{end}}</button>
</form>
{{end}}
</body></html>`))
