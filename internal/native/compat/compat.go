// Package compat serves the old /panel/api surface on top of /api/v1, so the
// current interface and scripts keep working against a native master. It
// knows no module: each one declares its own routes through Legacy().
package compat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// Handler serves every module's legacy routes by calling api in-process. Like
// the old panel it answers 200 with success=false for a failed call; refused
// credentials keep their 401/403 so callers can tell them from a bad request.
func Handler(mods []module.Module, api http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, m := range mods {
		for _, lr := range m.Legacy() {
			mux.Handle(lr.Method+" "+lr.Old, serve(lr, api))
		}
	}
	return mux
}

// refuse answers an old-panel failure the old way: 200 with success=false.
func refuse(w http.ResponseWriter, err error) {
	out := envelope{Msg: err.Error(), Obj: json.RawMessage("null")}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func serve(lr module.LegacyRoute, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := lr.New
		for _, seg := range strings.Split(lr.Old, "/") {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				name := seg[1 : len(seg)-1]
				path = strings.ReplaceAll(path, seg, r.PathValue(name))
			}
		}
		method := lr.NewMethod
		if method == "" {
			method = lr.Method
		}
		var reqBody []byte
		if lr.Rewrite != nil {
			reqBody, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var err error
			if method, path, reqBody, err = lr.Rewrite(method, path, reqBody); err != nil {
				refuse(w, err)
				return
			}
		}
		inner := r.Clone(r.Context())
		inner.Method = method
		inner.URL.Path, inner.URL.RawPath = path, ""
		inner.RequestURI = ""
		if lr.Rewrite != nil {
			inner.Body = io.NopCloser(bytes.NewReader(reqBody))
			inner.ContentLength = int64(len(reqBody))
		}

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, inner)
		body := rec.Body.Bytes()

		out := envelope{Obj: json.RawMessage("null")}
		switch {
		case rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rec.Code)
			_, _ = w.Write(body)
			return
		case rec.Code >= 200 && rec.Code < 300:
			out.Success = true
			if len(strings.TrimSpace(string(body))) > 0 {
				obj := body
				if lr.Wrap != nil {
					var err error
					if obj, err = lr.Wrap(body); err != nil {
						out.Success, out.Msg, obj = false, err.Error(), nil
					}
				}
				if obj != nil {
					out.Obj = obj
				}
			}
		default:
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			out.Msg = e.Error
			if out.Msg == "" {
				out.Msg = http.StatusText(rec.Code)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
