package masterd

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/native/apiserver"
	"github.com/mhsanaei/3x-ui/v3/internal/native/clients"
	"github.com/mhsanaei/3x-ui/v3/internal/native/inboundparam"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodespec"
	"github.com/mhsanaei/3x-ui/v3/internal/native/nodestate"
	"github.com/mhsanaei/3x-ui/v3/internal/native/subauth"
	"github.com/mhsanaei/3x-ui/v3/internal/native/usage"
)

// The old interface reads inbounds and clients as panel-wide tables with
// numeric ids. A native fleet stores them per node, in the node's spec and in
// the client records. These endpoints present the same data in the shapes the
// existing pages already parse (read-only; changes go through the native
// routes), so the operator keeps one panel while the fleet is a native one.
//
// A synthetic id is derived from the node and the tag: the pair is unique and
// stable, so a page can keep selecting the same inbound across refreshes.

// fleetInboundID maps a node and tag onto a stable positive int.
func fleetInboundID(node, tag string) int {
	h := uint32(2166136261) // FNV-1a over "node\x00tag"
	for _, s := range []string{node, "\x00", tag} {
		for i := 0; i < len(s); i++ {
			h ^= uint32(s[i])
			h *= 16777619
		}
	}
	if h == 0 {
		h = 1
	}
	return int(h &^ (1 << 31)) // positive
}

// fleetInbound is one inbound in the old table's shape.
type fleetInbound struct {
	ID             int                 `json:"id"`
	UserID         int                 `json:"userId"`
	Up             int64               `json:"up"`
	Down           int64               `json:"down"`
	Total          int64               `json:"total"`
	Remark         string              `json:"remark"`
	Enable         bool                `json:"enable"`
	ExpiryTime     int64               `json:"expiryTime"`
	Listen         string              `json:"listen"`
	Port           int                 `json:"port"`
	Protocol       string              `json:"protocol"`
	Settings       string              `json:"settings"`
	StreamSettings string              `json:"streamSettings"`
	Tag            string              `json:"tag"`
	Sniffing       string              `json:"sniffing"`
	NodeID         *int                `json:"nodeId"`
	Params         inboundparam.Params `json:"params,omitempty"`
	ClientStats    []fleetClientStat   `json:"clientStats"`
}

// fleetClientStat is one client's traffic row inside an inbound.
type fleetClientStat struct {
	ID         int    `json:"id"`
	InboundID  int    `json:"inboundId"`
	Enable     bool   `json:"enable"`
	Email      string `json:"email"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	ExpiryTime int64  `json:"expiryTime"`
	Total      int64  `json:"total"`
	Reset      int    `json:"reset"`
}

// fleetInbounds gathers every node's inbounds with their clients and traffic.
func (d *Daemon) fleetInbounds() []fleetInbound {
	specs := d.effectiveSpecs()
	sort.Slice(specs, func(i, j int) bool { return specs[i].Node < specs[j].Node })
	usageByEmail := map[string]int64{}
	for _, key := range d.store.State.Keys(usage.Table) {
		if rec, ok := d.store.State.Get(usage.Table, key); ok {
			var n int64
			_ = json.Unmarshal(rec["bytes"], &n)
			usageByEmail[key] = n
		}
	}
	records := clients.List(d.store.State)
	byEmailNode := map[string]clients.Client{}
	for _, c := range records {
		byEmailNode[c.Node+"\x00"+c.Email] = c
	}
	var out []fleetInbound
	for _, s := range specs {
		for _, ib := range s.Inbounds {
			id := fleetInboundID(s.Node, ib.Tag)
			fi := fleetInbound{ID: id, Remark: s.Node + "/" + ib.Tag, Enable: true}
			if p, ok := inboundparam.Get(d.store.State, s.Node, ib.Tag); ok {
				fi.Params = p
			}
			fi.Listen = ""
			fi.Port = ib.Port
			fi.Protocol = "vless"
			fi.Tag = s.Node + "/" + ib.Tag
			fi.Sniffing = `{"enabled":true,"destOverride":["http","tls","quic"]}`
			stream := map[string]any{"network": ib.Network}
			switch ib.Network {
			case "xhttp":
				x := map[string]any{"path": ib.Path}
				if ib.Host != "" {
					x["host"] = ib.Host
				}
				stream["xhttpSettings"] = x
			default:
				ws := map[string]any{"path": ib.Path}
				if ib.Host != "" {
					ws["headers"] = map[string]any{"Host": ib.Host}
				}
				stream["wsSettings"] = ws
			}
			rawStream, _ := json.Marshal(stream)
			fi.StreamSettings = string(rawStream)
			cl := make([]map[string]any, 0, len(ib.Clients))
			for i, c := range ib.Clients {
				if c.Reverse {
					continue // the reverse identity is not a user-visible client
				}
				rec := byEmailNode[s.Node+"\x00"+c.Email]
				enable := true
				if rec.Email != "" {
					enable = rec.Enable
				}
				cl = append(cl, map[string]any{
					"id": c.ID, "email": c.Email, "enable": enable,
					"limitIp": c.LimitIP, "totalGB": c.TotalBytes, "expiryTime": c.ExpiresAt,
					"flow": "", "subId": "",
				})
				fi.ClientStats = append(fi.ClientStats, fleetClientStat{
					ID: i + 1, InboundID: id, Enable: enable, Email: c.Email,
					Up: 0, Down: usageByEmail[c.Email], ExpiryTime: c.ExpiresAt, Total: c.TotalBytes,
				})
				fi.Up += 0
				fi.Down += usageByEmail[c.Email]
			}
			rawSettings, _ := json.Marshal(map[string]any{"clients": cl, "decryption": "none"})
			fi.Settings = string(rawSettings)
			out = append(out, fi)
		}
	}
	return out
}

// fleetStatus is a small, honest version of the old panel status: the fleet's
// own numbers, not a local core's.
func (d *Daemon) fleetStatus(w http.ResponseWriter, r *http.Request) {
	specs := d.effectiveSpecs()
	total := len(specs)
	reps := d.reportCounts(r)
	clientsN := 0
	seen := map[string]bool{}
	for _, c := range clients.List(d.store.State) {
		key := c.Node + "\x00" + c.Email
		if seen[key] {
			continue
		}
		seen[key] = true
		clientsN++
	}
	apiserver.JSON(w, http.StatusOK, map[string]any{
		"master":  d.cfg.Name,
		"nodes":   map[string]any{"total": total, "online": reps},
		"clients": map[string]any{"total": clientsN},
		"version": versionString(),
	})
}

// collectNodeStates is the fleet's view of every node (the /nodes/state data).
func (d *Daemon) collectNodeStates(r *http.Request) []nodestate.State {
	nodes := append([]string(nil), d.store.State.Keys(SpecTable)...)
	reps := nodestate.Collect(r.Context(), d.bus, d.cfg.Masters, 2000)
	return nodestate.Aggregate(nodes, reps, time.Now(), nodestate.DefaultStale)
}

// reportCounts asks the report source how many nodes reported recently.
func (d *Daemon) reportCounts(r *http.Request) int {
	states := d.collectNodeStates(r)
	n := 0
	for _, s := range states {
		if s.Online {
			n++
		}
	}
	return n
}

func versionString() string { return "" }

// serveFleetView answers the legacy-shaped aggregate paths.
func (d *Daemon) serveFleetView(mux *http.ServeMux) {
	mux.HandleFunc("GET /inbounds/list", func(w http.ResponseWriter, r *http.Request) {
		d.catchUp(r)
		apiserver.JSON(w, http.StatusOK, d.fleetInbounds())
	})
	mux.HandleFunc("GET /inbounds/list/slim", func(w http.ResponseWriter, r *http.Request) {
		d.catchUp(r)
		out := make([]map[string]any, 0)
		for _, in := range d.fleetInbounds() {
			out = append(out, map[string]any{
				"id": in.ID, "protocol": in.Protocol, "remark": in.Remark, "port": in.Port,
				"tag": in.Tag, "enable": in.Enable, "up": in.Up, "down": in.Down,
				"total": in.Total, "expiryTime": in.ExpiryTime, "nodeId": in.NodeID,
			})
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /inbounds/options", func(w http.ResponseWriter, r *http.Request) {
		d.catchUp(r)
		out := make([]map[string]any, 0)
		for _, in := range d.fleetInbounds() {
			out = append(out, map[string]any{
				"id": in.ID, "remark": in.Remark, "protocol": in.Protocol,
				"port": in.Port, "tag": in.Tag,
			})
		}
		apiserver.JSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /clients/list", func(w http.ResponseWriter, r *http.Request) {
		d.catchUp(r)
		apiserver.JSON(w, http.StatusOK, d.fleetClients())
	})
	mux.HandleFunc("GET /clients/list/paged", func(w http.ResponseWriter, r *http.Request) {
		d.catchUp(r)
		apiserver.JSON(w, http.StatusOK, d.fleetClientsPaged(r))
	})
	mux.HandleFunc("GET /clients/onlines", func(w http.ResponseWriter, r *http.Request) {
		out := make([]string, 0)
		for email := range d.presence.Where() {
			out = append(out, email)
		}
		sort.Strings(out)
		apiserver.JSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /nodes/list", func(w http.ResponseWriter, r *http.Request) {
		d.catchUp(r)
		apiserver.JSON(w, http.StatusOK, d.fleetNodes(r))
	})
	mux.HandleFunc("GET /server/status", d.fleetStatus)
	mux.HandleFunc("GET /status", d.fleetStatus)
}

func (d *Daemon) catchUp(r *http.Request) {
	if m := d.m.Load(); m != nil {
		_, _ = m.CatchUp(r.Context())
	}
}

// fleetClient is one client in the old clients-table shape, keyed by email
// across the fleet.
type fleetClient struct {
	ID         int                        `json:"id"`
	Email      string                     `json:"email"`
	UUID       string                     `json:"uuid"`
	Enable     bool                       `json:"enable"`
	TotalGB    int64                      `json:"totalGB"`
	ExpiryTime int64                      `json:"expiryTime"`
	LimitIP    int                        `json:"limitIp"`
	Group      string                     `json:"group"`
	SubID      string                     `json:"subId"`
	InboundIDs []int                      `json:"inboundIds"`
	Traffic    *fleetClientStat           `json:"traffic"`
	Nodes      []string                   `json:"nodes"`
	Comment    string                     `json:"comment"`
	Extra      map[string]json.RawMessage `json:"-"`
}

// fleetClients merges the per-node records into one row per email.
func (d *Daemon) fleetClients() []fleetClient {
	byEmail := map[string]*fleetClient{}
	for _, c := range clients.List(d.store.State) {
		row, ok := byEmail[c.Email]
		if !ok {
			row = &fleetClient{
				ID: len(byEmail) + 1, Email: c.Email, UUID: c.UUID,
				TotalGB: c.TotalGB, ExpiryTime: c.ExpiresAt, LimitIP: c.LimitIP,
				InboundIDs: []int{}, Nodes: []string{},
			}
			byEmail[c.Email] = row
		}
		row.Enable = row.Enable || c.Enable
		row.InboundIDs = append(row.InboundIDs, fleetInboundID(c.Node, c.Inbound))
		row.Nodes = append(row.Nodes, c.Node)
	}
	// the subscription login and traffic
	for _, token := range d.store.State.Keys(subauth.Table) {
		if rec, ok := subauth.Get(d.store.State, token); ok {
			if row, ok := byEmail[rec.Email]; ok {
				row.SubID = token
			}
		}
	}
	byEmailUsage := map[string]int64{}
	for _, key := range d.store.State.Keys(usage.Table) {
		if rec, ok := d.store.State.Get(usage.Table, key); ok {
			var n int64
			_ = json.Unmarshal(rec["bytes"], &n)
			byEmailUsage[key] = n
		}
	}
	out := make([]fleetClient, 0, len(byEmail))
	for _, row := range byEmail {
		sort.Ints(row.InboundIDs)
		sort.Strings(row.Nodes)
		row.Traffic = &fleetClientStat{
			Enable: row.Enable, Email: row.Email, Down: byEmailUsage[row.Email],
			ExpiryTime: row.ExpiryTime, Total: row.TotalGB,
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out
}

// fleetClientsPaged answers the page's {items,total,filtered,page,pageSize}
// shape with the same search the old panel used (a substring of the email).
func (d *Daemon) fleetClientsPaged(r *http.Request) map[string]any {
	all := d.fleetClients()
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	filtered := all
	if q != "" {
		filtered = filtered[:0:0]
		for _, c := range all {
			if strings.Contains(strings.ToLower(c.Email), q) {
				filtered = append(filtered, c)
			}
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if size <= 0 {
		size = 25
	}
	start := (page - 1) * size
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + size
	if end > len(filtered) {
		end = len(filtered)
	}
	items := filtered[start:end]
	var up, down int64
	groups := map[string]bool{}
	for _, c := range all {
		if c.Traffic != nil {
			down += c.Traffic.Down
		}
		if c.Group != "" {
			groups[c.Group] = true
		}
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	return map[string]any{
		"items": items, "total": len(all), "filtered": len(filtered),
		"page": page, "pageSize": size,
		"summary": map[string]any{"up": up, "down": down, "total": len(all)},
		"groups":  names,
	}
}

// fleetNodes answers the nodes table's shape from the fleet's own state.
func (d *Daemon) fleetNodes(r *http.Request) []map[string]any {
	states := d.collectNodeStates(r)
	specs := d.specs()
	byNode := map[string]nodespec.Spec{}
	inboundCount := map[string]int{}
	clientCount := map[string]int{}
	for _, s := range specs {
		byNode[s.Node] = s
		inboundCount[s.Node] = len(s.Inbounds)
		for _, ib := range s.Inbounds {
			clientCount[s.Node] += len(ib.Clients)
		}
	}
	out := make([]map[string]any, 0, len(states))
	for i, st := range states {
		row := map[string]any{
			"id": i + 1, "name": st.Node, "address": st.Node, "enable": st.HasSpec,
			"status":       map[bool]string{true: "online", false: "offline"}[st.Online],
			"inboundCount": inboundCount[st.Node], "clientCount": clientCount[st.Node],
			"onlineCount": len(st.OnlineUsers), "lastHeartbeat": st.LastSeen / 1000,
			"maintenance": st.Draining, "drainEnabled": st.Draining,
			"drainActive": st.DrainActive, "drainSince": st.DrainSince,
			"remark": "", "scheme": "https", "port": 443,
		}
		if st.Health != nil {
			if st.Health.MemTotal > 0 {
				row["memPct"] = float64(st.Health.MemUsed) / float64(st.Health.MemTotal) * 100
			}
			row["uptimeSecs"] = st.Health.Uptime
			row["panelVersion"] = st.Health.Version
		}
		out = append(out, row)
	}
	return out
}
