package vnet

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// API is the users' side, under /api/v1/, authenticated by the user's token.
func API(s *Store, where func() map[string]string) http.Handler {
	mux := http.NewServeMux()
	auth := func(h func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			u, ok := s.UserByToken(token)
			if !ok {
				fail(w, http.StatusUnauthorized, errors.New("нужен токен"))
				return
			}
			h(w, r, u)
		}
	}
	mux.HandleFunc("GET /api/v1/me", auth(func(w http.ResponseWriter, _ *http.Request, u User) {
		m, _ := s.NetworkOf(u.ID)
		ok(w, map[string]any{"id": u.ID, "ip": u.IP, "network": m.Network, "role": m.Role, "node": where()[u.Email]})
	}))
	mux.HandleFunc("POST /api/v1/network", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		id, err := s.CreateNetwork(r.Context(), u.ID, body.Title)
		reply(w, map[string]any{"network": id}, err)
	}))
	mux.HandleFunc("GET /api/v1/network", auth(func(w http.ResponseWriter, _ *http.Request, u User) {
		m, in := s.NetworkOf(u.ID)
		if !in {
			fail(w, http.StatusNotFound, ErrNoNetwork)
			return
		}
		online := where()
		var members []map[string]any
		for _, mm := range s.Members(m.Network) {
			peer, _ := s.User(mm.User)
			members = append(members, map[string]any{"id": mm.User, "ip": peer.IP, "role": mm.Role, "node": online[peer.Email]})
		}
		ok(w, map[string]any{"network": m.Network, "role": m.Role, "members": members})
	}))
	mux.HandleFunc("DELETE /api/v1/network", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.DeleteNetwork(r.Context(), u.ID))
	}))
	mux.HandleFunc("POST /api/v1/network/leave", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.Leave(r.Context(), u.ID))
	}))
	mux.HandleFunc("POST /api/v1/network/transfer", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ To string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, nil, s.Transfer(r.Context(), u.ID, body.To))
	}))
	mux.HandleFunc("POST /api/v1/network/invites", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct {
			TTL  string
			Uses int
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ttl, err := time.ParseDuration(body.TTL)
		if err != nil || ttl <= 0 {
			ttl = 24 * time.Hour
		}
		code, err := s.Invite(r.Context(), u.ID, ttl, body.Uses)
		reply(w, map[string]any{"code": code}, err)
	}))
	mux.HandleFunc("DELETE /api/v1/network/invites/{code}", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.Revoke(r.Context(), u.ID, r.PathValue("code")))
	}))
	mux.HandleFunc("POST /api/v1/network/join", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, nil, s.Join(r.Context(), u.ID, body.Code))
	}))
	mux.HandleFunc("DELETE /api/v1/network/members/{user}", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		reply(w, nil, s.Kick(r.Context(), u.ID, r.PathValue("user")))
	}))
	mux.HandleFunc("PATCH /api/v1/network/members/{user}", auth(func(w http.ResponseWriter, r *http.Request, u User) {
		var body struct{ Role string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		reply(w, nil, s.SetRole(r.Context(), u.ID, r.PathValue("user"), body.Role))
	}))
	return mux
}

func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		ok(w, v)
	case errors.Is(err, ErrInNetwork), errors.Is(err, ErrOwnerLeaves):
		fail(w, http.StatusConflict, err)
	case errors.Is(err, ErrForbidden):
		fail(w, http.StatusForbidden, err)
	case errors.Is(err, ErrNoNetwork), errors.Is(err, ErrNoUser), errors.Is(err, ErrBadInvite):
		fail(w, http.StatusNotFound, err)
	default:
		fail(w, http.StatusInternalServerError, err)
	}
}
