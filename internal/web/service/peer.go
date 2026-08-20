package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/cluster"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
)

// PeerHealthPatch is the observed half of a peer row, written back after a probe.
type PeerHealthPatch struct {
	Status        string `json:"status"`
	LastHeartbeat int64  `json:"lastHeartbeat"`
	LatencyMs     int    `json:"latencyMs"`
	PublicKey     string `json:"publicKey"`
	IsSelf        bool   `json:"isSelf"`
	LastError     string `json:"lastError"`
}

type PeerService struct{}

func (s *PeerService) GetAll() ([]*model.MasterPeer, error) {
	db := database.GetDB()
	var peers []*model.MasterPeer
	if err := db.Order("id asc").Find(&peers).Error; err != nil {
		return nil, err
	}
	return peers, nil
}

func (s *PeerService) GetById(id int) (*model.MasterPeer, error) {
	db := database.GetDB()
	peer := &model.MasterPeer{}
	if err := db.Where("id = ?", id).First(peer).Error; err != nil {
		return nil, err
	}
	return peer, nil
}

func (s *PeerService) normalize(p *model.MasterPeer) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return common.NewError("peer name is required")
	}
	domain, err := netsafe.NormalizeHost(p.Domain)
	if err != nil {
		return common.NewError(err.Error())
	}
	p.Domain = domain
	if p.Port <= 0 || p.Port > 65535 {
		return common.NewError("peer port must be 1-65535")
	}
	if p.Scheme != "http" && p.Scheme != "https" {
		p.Scheme = "https"
	}
	p.SubPath = cluster.NormalizeSubPath(p.SubPath)
	p.BasePath = normalizeBasePath(p.BasePath)
	p.Remark = strings.TrimSpace(p.Remark)

	ips := make([]string, 0, len(p.Ips))
	for _, raw := range p.Ips {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		// Literal addresses only: a hostname here would pass validation and then
		// be silently dropped when the fallback header is rendered.
		ip := net.ParseIP(raw)
		if ip == nil {
			return common.NewError("peer IP " + raw + " is not a valid IP address")
		}
		ips = append(ips, ip.String())
	}
	p.Ips = ips
	return nil
}

func (s *PeerService) Create(p *model.MasterPeer) error {
	if err := s.normalize(p); err != nil {
		return err
	}
	p.Status = "unknown"
	p.PublicKey = ""
	// GORM leaves a zero value out of the INSERT when the column declares a
	// default and then refreshes the field from the row it wrote, so Create
	// turns a peer added as disabled into an enabled one. Remember the intent
	// before it is overwritten and write it back explicitly.
	wantEnabled := p.Enable
	db := database.GetDB()
	if err := db.Create(p).Error; err != nil {
		return err
	}
	if wantEnabled {
		return nil
	}
	if err := db.Model(model.MasterPeer{}).Where("id = ?", p.Id).Update("enable", false).Error; err != nil {
		return err
	}
	p.Enable = false
	return nil
}

// Update rewrites the operator-owned columns only. Observed state (status,
// latency, learned public key) belongs to the health job and would otherwise be
// wiped on every edit.
func (s *PeerService) Update(id int, in *model.MasterPeer) error {
	if err := s.normalize(in); err != nil {
		return err
	}
	// Serialized by hand: GORM does not apply a field serializer to a map-based
	// Updates, the same reason Node.InboundTags is written this way.
	ipsJSON, err := json.Marshal(in.Ips)
	if err != nil {
		return err
	}
	db := database.GetDB()
	existing := &model.MasterPeer{}
	if err := db.Where("id = ?", id).First(existing).Error; err != nil {
		return err
	}
	return db.Model(model.MasterPeer{}).Where("id = ?", id).Updates(map[string]any{
		"name":                  in.Name,
		"remark":                in.Remark,
		"scheme":                in.Scheme,
		"domain":                in.Domain,
		"port":                  in.Port,
		"sub_path":              in.SubPath,
		"base_path":             in.BasePath,
		"ips":                   string(ipsJSON),
		"enable":                in.Enable,
		"allow_private_address": in.AllowPrivateAddress,
		"is_self":               in.IsSelf,
	}).Error
}

func (s *PeerService) Delete(id int) error {
	return database.GetDB().Where("id = ?", id).Delete(&model.MasterPeer{}).Error
}

func (s *PeerService) SetEnable(id int, enable bool) error {
	return database.GetDB().Model(model.MasterPeer{}).Where("id = ?", id).Update("enable", enable).Error
}

// Probe checks one peer's subscription server and reports what to persist. A
// peer whose published key equals ours is this very panel, which must never
// advertise itself as its own fallback.
func (s *PeerService) Probe(ctx context.Context, client *http.Client, p *model.MasterPeer) PeerHealthPatch {
	patch := PeerHealthPatch{LastHeartbeat: time.Now().Unix(), IsSelf: p.IsSelf}
	res := cluster.Probe(ctx, client, cluster.ProbeTarget{
		Scheme:       p.Scheme,
		Domain:       p.Domain,
		Port:         p.Port,
		SubPath:      p.SubPath,
		AllowPrivate: p.AllowPrivateAddress,
	})
	patch.LatencyMs = res.LatencyMs
	if res.Err != nil {
		patch.Status = "offline"
		patch.LastError = res.Err.Error()
		return patch
	}
	patch.Status = "online"
	patch.PublicKey = res.PublicKey
	if selfKey, err := (&SettingService{}).GetSubSignPublicKey(); err == nil && selfKey != "" && selfKey == res.PublicKey {
		patch.IsSelf = true
	}
	return patch
}

func (s *PeerService) UpdateHealth(id int, patch PeerHealthPatch) error {
	updates := map[string]any{
		"status":         patch.Status,
		"last_heartbeat": patch.LastHeartbeat,
		"latency_ms":     patch.LatencyMs,
		"last_error":     patch.LastError,
		"is_self":        patch.IsSelf,
	}
	if patch.PublicKey != "" {
		updates["public_key"] = patch.PublicKey
	}
	return database.GetDB().Model(model.MasterPeer{}).Where("id = ?", id).Updates(updates).Error
}

// LiveEndpoints returns the peers a client can currently be pointed at, ordered
// by measured latency so the closest fallback is offered first. Rows describing
// this panel are dropped unless includeSelf, which the payload's master list
// wants and the fallback headers do not.
func (s *PeerService) LiveEndpoints(includeSelf bool) ([]cluster.Endpoint, error) {
	db := database.GetDB()
	var peers []*model.MasterPeer
	q := db.Where("enable = ?", true).Where("status = ?", "online")
	if !includeSelf {
		q = q.Where("is_self = ?", false)
	}
	if err := q.Order("latency_ms asc").Order("id asc").Find(&peers).Error; err != nil {
		return nil, err
	}
	out := make([]cluster.Endpoint, 0, len(peers))
	for _, p := range peers {
		out = append(out, cluster.Endpoint{Domain: p.Domain, Ips: p.Ips})
	}
	return out, nil
}
