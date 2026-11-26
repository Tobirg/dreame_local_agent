package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// The main token is the owner's password: the panel calls it that. It may be changed from the panel; the module
// tokens are derived from module_secret, which keeps the old value the first time, so modules keep working.
const minPasswordLen, maxPasswordLen = 8, 128

func validPassword(p string) error {
	if len(p) < minPasswordLen || len(p) > maxPasswordLen {
		return errors.New("password: 8 to 128 characters")
	}
	if strings.TrimSpace(p) != p {
		return errors.New("password must not start or end with a space")
	}
	for _, r := range p {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return errors.New("password: printable Latin letters, digits and symbols only")
		}
	}
	return nil
}

func (a *agent) password() string {
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	return a.cfg.Token
}

// moduleSecret is the key module tokens are derived from: module_secret, or the password for configs written
// before the password could be changed.
func (a *agent) moduleSecret() string {
	a.tokensMu.RLock()
	defer a.tokensMu.RUnlock()
	if a.cfg.ModuleSecret != "" {
		return a.cfg.ModuleSecret
	}
	return a.cfg.Token
}

// changePassword writes the new password to the config file first and only then uses it, so a failed write
// leaves the old password working.
func (a *agent) changePassword(current, next string) error {
	if err := validPassword(next); err != nil {
		return err
	}
	a.tokensMu.Lock()
	defer a.tokensMu.Unlock()
	if subtle.ConstantTimeCompare([]byte(current), []byte(a.cfg.Token)) != 1 {
		return errWrongPassword
	}
	secret := a.cfg.ModuleSecret
	if secret == "" {
		secret = a.cfg.Token
	}
	if err := a.saveConfigFields(map[string]any{"token": next, "module_secret": secret}); err != nil {
		return err
	}
	a.cfg.Token, a.cfg.ModuleSecret = next, secret
	return nil
}

var errWrongPassword = errors.New("current password is wrong")

func (a *agent) passwordRoute(r *http.Request, send func(int, any)) {
	if r.Method != "POST" {
		send(405, map[string]string{"error": "POST required"})
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		send(400, map[string]string{"error": "invalid request"})
		return
	}
	if err := a.changePassword(req.Current, req.New); err != nil {
		code := 400
		if errors.Is(err, errWrongPassword) {
			code = 403
			time.Sleep(time.Second)
		} else if !strings.HasPrefix(err.Error(), "password") {
			code = 500
		}
		send(code, map[string]string{"error": err.Error()})
		return
	}
	send(200, map[string]any{"result": map[string]bool{"changed": true}})
}

// authFailures lets one failed sign-in through at a time, each held for a second, so a password cannot be
// guessed faster than about once a second; signed-in requests are not slowed. Extra failures at once get 429.
func (a *agent) refuseUnauthorized(send func(int, any)) {
	if a.authFailures == nil { // tests that do not check the limit
		send(401, map[string]string{"error": "unauthorized"})
		return
	}
	select {
	case a.authFailures <- struct{}{}:
		time.Sleep(a.authDelay)
		<-a.authFailures
		send(401, map[string]string{"error": "unauthorized"})
	default:
		send(429, map[string]string{"error": "too many wrong passwords, wait a second"})
	}
}
