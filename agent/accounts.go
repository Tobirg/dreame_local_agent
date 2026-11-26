package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Accounts: people sign in to the panel with a name and a password and get a session. Two roles:
// "admin" can do everything; "user" runs the vacuum (cleaning, map, station, camera, the robot's own settings)
// but not the agent itself: no /api/v1/system/* (password of others, tokens, ADB, update, Wi-Fi of the agent,
// video code), no module install/remove, no logs, no voice packs, no Wi-Fi setup or reboot through /rpc.
// The account "admin" is the agent password (config "token"), so existing installs keep working; other
// accounts keep only a PBKDF2 hash. Scoped tokens stay for programs (Home Assistant).
const (
	adminAccount    = "admin"
	sessionTTL      = 90 * 24 * time.Hour
	maxSessions     = 64
	maxAccounts     = 16
	pbkdf2Rounds    = 100000
	sessionsFile    = "sessions.json"
	roleAdmin       = "admin"
	roleUser        = "user"
	accountNameRule = "name: 1-32 Latin letters, digits, '.', '_' or '-'"
)

type account struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Salt    string `json:"salt"`
	Hash    string `json:"hash"`
	Created string `json:"created"`
}

type session struct {
	Account string    `json:"account"`
	Expires time.Time `json:"expires"`
}

type sessionStore struct {
	mu   sync.Mutex
	byID map[string]session // key: sha256 of the session token
	path string
}

var accountNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

func passwordHash(salt []byte, password string) string {
	key, _ := pbkdf2.Key(sha256.New, password, salt, pbkdf2Rounds, 32)
	return hex.EncodeToString(key)
}

func tokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// roleOf returns the role of an account, or "" when it does not exist.
func (a *agent) roleOf(name string) string {
	if name == adminAccount {
		return roleAdmin
	}
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	for _, u := range a.cfg.Users {
		if u.Name == name {
			return u.Role
		}
	}
	return ""
}

// checkAccount compares a name and password; the admin account is the agent password.
func (a *agent) checkAccount(name, password string) bool {
	if name == adminAccount {
		return subtle.ConstantTimeCompare([]byte(password), []byte(a.password())) == 1
	}
	a.tokensMu.RLock()
	var found *account
	for i := range a.cfg.Users {
		if a.cfg.Users[i].Name == name {
			u := a.cfg.Users[i]
			found = &u
		}
	}
	a.tokensMu.RUnlock()
	if found == nil {
		passwordHash([]byte("no such account"), password) // same time as a real check
		return false
	}
	salt, err := hex.DecodeString(found.Salt)
	return err == nil && subtle.ConstantTimeCompare([]byte(passwordHash(salt, password)), []byte(found.Hash)) == 1
}

func (s *sessionStore) load() {
	s.byID = map[string]session{}
	if s.path == "" {
		return
	}
	if b, err := readLimited(s.path, 1<<20); err == nil {
		_ = json.Unmarshal(b, &s.byID)
	}
}

// saveLocked writes the sessions (only their sha256 ids) next to the config; s.mu must be held.
func (s *sessionStore) saveLocked() {
	if s.path == "" {
		return
	}
	b, _ := json.Marshal(s.byID)
	_ = atomicPrivate(s.path, b)
}

func (a *agent) sessions() *sessionStore {
	a.sessionsOnce.Do(func() {
		if a.configPath != "" {
			a.sess.path = filepath.Join(filepath.Dir(a.configPath), sessionsFile)
		}
		a.sess.load()
	})
	return &a.sess
}

func (a *agent) newSession(name string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	s := a.sessions()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	oldest := ""
	for id, v := range s.byID {
		if now.After(v.Expires) {
			delete(s.byID, id)
		} else if oldest == "" || v.Expires.Before(s.byID[oldest].Expires) {
			oldest = id
		}
	}
	if len(s.byID) >= maxSessions {
		delete(s.byID, oldest)
	}
	s.byID[tokenID(token)] = session{Account: name, Expires: now.Add(sessionTTL)}
	s.saveLocked()
	return token, nil
}

// sessionAccount returns the account of a valid session token, or "".
func (a *agent) sessionAccount(token string) string {
	if len(token) != 64 {
		return ""
	}
	s := a.sessions()
	s.mu.Lock()
	v, ok := s.byID[tokenID(token)]
	s.mu.Unlock()
	if !ok || time.Now().After(v.Expires) || a.roleOf(v.Account) == "" {
		return ""
	}
	return v.Account
}

// endSessions drops the sessions of an account, except keep (a token id) when it is not "".
func (a *agent) endSessions(name, keep string) {
	s := a.sessions()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.byID {
		if v.Account == name && id != keep {
			delete(s.byID, id)
		}
	}
	s.saveLocked()
}

func (a *agent) endSession(token string) {
	s := a.sessions()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, tokenID(token))
	s.saveLocked()
}

// whoAmI names the account behind an Authorization header: a session, or "admin" for the agent password.
func (a *agent) whoAmI(auth string) string {
	presented, _ := strings.CutPrefix(auth, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(presented), []byte(a.password())) == 1 {
		return adminAccount
	}
	return a.sessionAccount(presented)
}

func validAccountPassword(p string) error {
	if err := validPassword(p); err != nil {
		return adbRefusal(err.Error())
	}
	return nil
}

func (a *agent) createAccount(name, role, password string) error {
	if !accountNameRe.MatchString(name) {
		return adbRefusal(accountNameRule)
	}
	if role != roleAdmin && role != roleUser {
		return adbRefusal("role must be admin or user")
	}
	if err := validAccountPassword(password); err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := passwordHash(salt, password)
	a.tokensMu.Lock()
	defer a.tokensMu.Unlock()
	if strings.EqualFold(name, adminAccount) {
		return adbRefusal("account already exists")
	}
	for _, u := range a.cfg.Users {
		if strings.EqualFold(u.Name, name) {
			return adbRefusal("account already exists")
		}
	}
	if len(a.cfg.Users) >= maxAccounts {
		return adbRefusal("too many accounts")
	}
	users := append(append([]account(nil), a.cfg.Users...), account{Name: name, Role: role, Salt: hex.EncodeToString(salt), Hash: hash, Created: time.Now().UTC().Format(time.RFC3339)})
	if err := a.saveConfigField("users", users); err != nil {
		return err
	}
	a.cfg.Users = users
	return nil
}

func (a *agent) deleteAccount(name string) error {
	if name == adminAccount {
		return adbRefusal("the admin account cannot be removed")
	}
	a.tokensMu.Lock()
	users := []account{}
	for _, u := range a.cfg.Users {
		if u.Name != name {
			users = append(users, u)
		}
	}
	if len(users) == len(a.cfg.Users) {
		a.tokensMu.Unlock()
		return adbRefusal("no such account")
	}
	err := a.saveConfigField("users", users)
	if err == nil {
		a.cfg.Users = users
	}
	a.tokensMu.Unlock()
	if err == nil {
		a.endSessions(name, "")
	}
	return err
}

// setAccountPassword changes the password of an account other than admin (admin: changePassword).
func (a *agent) setAccountPassword(name, current, next string) error {
	if err := validAccountPassword(next); err != nil {
		return err
	}
	if !a.checkAccount(name, current) {
		return errWrongPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := passwordHash(salt, next)
	a.tokensMu.Lock()
	defer a.tokensMu.Unlock()
	users := append([]account(nil), a.cfg.Users...)
	for i := range users {
		if users[i].Name == name {
			users[i].Salt, users[i].Hash = hex.EncodeToString(salt), hash
			if err := a.saveConfigField("users", users); err != nil {
				return err
			}
			a.cfg.Users = users
			return nil
		}
	}
	return adbRefusal("no such account")
}

func (a *agent) listAccounts() []map[string]string {
	out := []map[string]string{{"name": adminAccount, "role": roleAdmin, "created": ""}}
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	for _, u := range a.cfg.Users {
		out = append(out, map[string]string{"name": u.Name, "role": u.Role, "created": u.Created})
	}
	return out
}

// loginRoute needs no token: a wrong name or password goes through the same once-a-second limit.
func (a *agent) loginRoute(r *http.Request, send func(int, any)) {
	if r.Method != "POST" {
		send(405, map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		send(400, map[string]string{"error": "invalid request"})
		return
	}
	if req.Name == "" {
		req.Name = adminAccount
	}
	if !a.checkAccount(req.Name, req.Password) {
		a.refuseUnauthorized(send)
		return
	}
	token, err := a.newSession(req.Name)
	if err != nil {
		send(500, map[string]string{"error": "cannot start a session"})
		return
	}
	send(200, map[string]any{"result": map[string]string{"token": token, "name": req.Name, "role": a.roleOf(req.Name)}})
}

// accountRoutes: /api/v1/account (who am I, own password, sign out) for every account;
// /api/v1/system/accounts for admins only (scoped tokens and users never reach /api/v1/system).
func (a *agent) accountRoutes(r *http.Request, send func(int, any)) bool {
	p := r.URL.Path
	auth := r.Header.Get("Authorization")
	fail := func(err error) {
		var refused adbRefusal
		switch {
		case errors.Is(err, errWrongPassword):
			time.Sleep(time.Second)
			send(403, map[string]string{"error": err.Error()})
		case errors.As(err, &refused):
			send(400, map[string]string{"error": err.Error()})
		default:
			send(500, map[string]string{"error": err.Error()})
		}
	}
	switch {
	case p == "/api/v1/account" && r.Method == "GET":
		name := a.whoAmI(auth)
		send(200, map[string]any{"result": map[string]string{"name": name, "role": a.roleOf(name)}})
	case p == "/api/v1/account/logout" && r.Method == "POST":
		presented, _ := strings.CutPrefix(auth, "Bearer ")
		a.endSession(presented)
		send(200, map[string]any{"result": map[string]bool{"signed_out": true}})
	case p == "/api/v1/account/password" && r.Method == "POST":
		var req struct {
			Current string `json:"current"`
			New     string `json:"new"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
			send(400, map[string]string{"error": "invalid request"})
			return true
		}
		name := a.whoAmI(auth)
		var err error
		if name == adminAccount {
			if err = a.changePassword(req.Current, req.New); err != nil && !errors.Is(err, errWrongPassword) && strings.HasPrefix(err.Error(), "password") {
				err = adbRefusal(err.Error())
			}
		} else {
			err = a.setAccountPassword(name, req.Current, req.New)
		}
		if err != nil {
			fail(err)
			return true
		}
		presented, _ := strings.CutPrefix(auth, "Bearer ")
		a.endSessions(name, tokenID(presented)) // other devices sign in again
		send(200, map[string]any{"result": map[string]bool{"changed": true}})
	case p == "/api/v1/system/accounts" && r.Method == "GET":
		send(200, map[string]any{"result": a.listAccounts()})
	case p == "/api/v1/system/accounts" && r.Method == "POST":
		var req struct {
			Name     string `json:"name"`
			Role     string `json:"role"`
			Password string `json:"password"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
			send(400, map[string]string{"error": "invalid request"})
			return true
		}
		if err := a.createAccount(req.Name, req.Role, req.Password); err != nil {
			fail(err)
			return true
		}
		send(200, map[string]any{"result": a.listAccounts()})
	case strings.HasPrefix(p, "/api/v1/system/accounts/") && r.Method == "DELETE":
		name := strings.TrimPrefix(p, "/api/v1/system/accounts/")
		if name == a.whoAmI(auth) {
			fail(adbRefusal("you cannot remove the account you are signed in with"))
			return true
		}
		if err := a.deleteAccount(name); err != nil {
			fail(err)
			return true
		}
		send(200, map[string]any{"result": a.listAccounts()})
	case p == "/api/v1/account" || strings.HasPrefix(p, "/api/v1/account/") || p == "/api/v1/system/accounts" || strings.HasPrefix(p, "/api/v1/system/accounts/"):
		send(405, map[string]string{"error": "method not allowed"})
	default:
		return false
	}
	return true
}

// userRoute says whether the "user" role may use this request; /rpc is narrowed after its body is parsed.
func userRoute(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/v1/system/"), p == "/api/v1/logs", p == "/api/v1/firmware_message":
		return false
	case strings.HasPrefix(p, "/api/v1/modules"):
		return r.Method == "GET" // the list, for module panels; no install, update or removal
	case p == "/api/v1/voice/install", p == "/api/v1/voice/remove":
		return false
	case p == "/api/v1/ui":
		return r.Method == "GET"
	}
	return true
}

// userRPCDenied lists /rpc methods that change the agent or the robot's network rather than the vacuum.
var userRPCDenied = map[string]bool{"local.wifi_setup": true, "local.reboot": true}
