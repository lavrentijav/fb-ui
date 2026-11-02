package masterd

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/pool"
)

// netIPs answers this master's pool addresses: what it announces, whether an
// operator set them by hand, what it detects on its own, and the addresses the
// fleet currently calls unreachable.
func (d *Daemon) netIPs(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	_, _ = m.CatchUp(r.Context())
	cur := []string{}
	if rec, ok := d.store.State.Get(pool.MastersTable, d.cfg.Name); ok {
		_ = json.Unmarshal(rec["ips"], &cur)
	}
	manual, _ := pool.Manual(d.store.State, d.cfg.Name)
	apiserver.JSON(w, http.StatusOK, map[string]any{
		"name": d.cfg.Name, "ips": cur, "manual": manual,
		"detected": pool.DetectIPs(), "failing": d.failing(),
	})
}

// setNetIPs sets an operator's address list ("auto": true goes back to
// detection).
func (d *Daemon) setNetIPs(w http.ResponseWriter, r *http.Request) {
	m := d.m.Load()
	if m == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Ips  []string `json:"ips"`
		Auto bool     `json:"auto"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var err error
	if body.Auto {
		ips := d.cfg.PublicIPs
		if len(ips) == 0 && d.cfg.PublicIP != "" {
			ips = []string{d.cfg.PublicIP}
		}
		if len(ips) == 0 {
			ips = pool.DetectIPs()
		}
		err = pool.Announce(r.Context(), m, d.store.State, ips)
	} else {
		err = pool.SetManual(r.Context(), m, d.store.State, body.Ips)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	apiserver.JSON(w, http.StatusOK, map[string]any{"status": "saved"})
}

// netHealth answers the addresses whose checks failed on this master and how
// many times in a row.
func (d *Daemon) netHealth(w http.ResponseWriter, _ *http.Request) {
	apiserver.JSON(w, http.StatusOK, map[string]any{"failing": d.failing()})
}

func (d *Daemon) failing() map[string]int {
	if p := d.pool.Load(); p != nil {
		return p.Failing()
	}
	return map[string]int{}
}
