// Package audit decides what an operator action looks like in the log. It is
// free of any web framework or database so another host can reuse the rules.
package audit

import (
	"net/http"
	"strings"
)

// Entry is one thing an operator did.
type Entry struct {
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
	IP     string `json:"ip"`
}

// Recordable says whether a request is an operator change worth keeping:
// anything that is not a read. Reads and the audit log itself stay out, as do
// the high-frequency calls the panel makes on its own.
func Recordable(method, path string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	for _, p := range noisy {
		if strings.HasSuffix(path, p) {
			return false
		}
	}
	return true
}

var noisy = []string{
	"/server/status",
	"/csrf-token",
	"/node/heartbeat",
}

// Actor names who acted: the logged-in user, or the token's scope when an API
// token did.
func Actor(user, tokenScope string) string {
	switch {
	case user != "":
		return user
	case tokenScope != "":
		return "token:" + tokenScope
	}
	return "unknown"
}
