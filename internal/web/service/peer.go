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

// PeerHealthPatch is the observed half of a master row, written back after a
// probe of its subscription server.
type PeerHealthPatch struct {
	Status        string `json:"status"`
	LastHeartbeat int64  `json:"lastHeartbeat"`
	LatencyMs     int    `json:"latencyMs"`
	PublicKey     string `json:"publicKey"`
	IsSelf        bool   `json:"isSelf"`
	LastError     string `json:"lastError"`
}

// PeerMutationRequest is the master write contract. Masters share model.Node
// with controlled nodes, whose validation demands panel credentials a master
// row never has, so the two roles cannot share one bind target.
type PeerMutationRequest struct {
	Id                  int      `json:"id" form:"id"`
	Name                string   `json:"name" form:"name" validate:"required" example:"eu-sub-2"`
	Remark              string   `json:"remark" form:"remark"`
	Scheme              string   `json:"scheme" form:"scheme" validate:"omitempty,oneof=http https" example:"https"`
	SubDomain           string   `json:"subDomain" form:"subDomain" validate:"required" example:"sub2.example.com"`
	SubPort             int      `json:"subPort" form:"subPort" validate:"gte=1,lte=65535" example:"2096"`
	SubPath             string   `json:"subPath" form:"subPath" example:"/sub/"`
	BasePath            string   `json:"basePath" form:"basePath" example:"/"`
	SubIps              []string `json:"subIps" form:"subIps"`
	Enable              bool     `json:"enable" form:"enable" example:"true"`
	AllowPrivateAddress bool     `json:"allowPrivateAddress" form:"allowPrivateAddress" example:"false"`
	IsSelf              bool     `json:"isSelf" form:"isSelf" example:"false"`
}

func (r *PeerMutationRequest) ToNode() *model.Node {
	return &model.Node{
		Id:                  r.Id,
		Name:                r.Name,
		Remark:              r.Remark,
		Scheme:              r.Scheme,
		SubDomain:           r.SubDomain,
		SubPort:             r.SubPort,
		SubPath:             r.SubPath,
		BasePath:            r.BasePath,
		SubIps:              r.SubIps,
		Enable:              r.Enable,
		AllowPrivateAddress: r.AllowPrivateAddress,
		IsSelf:              r.IsSelf,
		Role:                model.NodeRoleMaster,
	}
}

// PeerService manages the master half of the node registry: sibling panels that
// serve the same subscriptions and are advertised to clients as fallbacks. They
// live in the same table as controlled nodes and differ only by role.
type PeerService struct{}

func (s *PeerService) GetAll() ([]*model.Node, error) {
	db := database.GetDB()
	var peers []*model.Node
	if err := db.Where("role = ?", model.NodeRoleMaster).Order("id asc").Find(&peers).Error; err != nil {
		return nil, err
	}
	return peers, nil
}

func (s *PeerService) GetById(id int) (*model.Node, error) {
	db := database.GetDB()
	peer := &model.Node{}
	if err := db.Where("id = ? AND role = ?", id, model.NodeRoleMaster).First(peer).Error; err != nil {
		return nil, err
	}
	return peer, nil
}

func (s *PeerService) normalize(p *model.Node) error {
	p.Role = model.NodeRoleMaster
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return common.NewError("peer name is required")
	}
	domain, err := netsafe.NormalizeHost(p.SubDomain)
	if err != nil {
		return common.NewError(err.Error())
	}
	p.SubDomain = domain
	// A master is reached at its subscription server; the panel address tracks
	// it so one row keeps describing exactly one panel.
	p.Address = domain
	if p.SubPort <= 0 || p.SubPort > 65535 {
		return common.NewError("peer subscription port must be 1-65535")
	}
	if p.Scheme != "http" && p.Scheme != "https" {
		p.Scheme = "https"
	}
	p.SubPath = cluster.NormalizeSubPath(p.SubPath)
	p.BasePath = normalizeBasePath(p.BasePath)
	p.Remark = strings.TrimSpace(p.Remark)

	ips := make([]string, 0, len(p.SubIps))
	for _, raw := range p.SubIps {
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
	p.SubIps = ips
	return nil
}

func (s *PeerService) Create(p *model.Node) error {
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
	if err := db.Model(model.Node{}).Where("id = ?", p.Id).Update("enable", false).Error; err != nil {
		return err
	}
	p.Enable = false
	return nil
}

// Update rewrites the operator-owned columns only. Observed state (status,
// latency, learned public key) belongs to the health job and would otherwise be
// wiped on every edit.
func (s *PeerService) Update(id int, in *model.Node) error {
	if err := s.normalize(in); err != nil {
		return err
	}
	// Serialized by hand: GORM does not apply a field serializer to a map-based
	// Updates, the same reason Node.InboundTags is written this way.
	ipsJSON, err := json.Marshal(in.SubIps)
	if err != nil {
		return err
	}
	db := database.GetDB()
	existing := &model.Node{}
	if err := db.Where("id = ? AND role = ?", id, model.NodeRoleMaster).First(existing).Error; err != nil {
		return err
	}
	return db.Model(model.Node{}).Where("id = ?", id).Updates(map[string]any{
		"name":                  in.Name,
		"remark":                in.Remark,
		"scheme":                in.Scheme,
		"address":               in.Address,
		"sub_domain":            in.SubDomain,
		"sub_port":              in.SubPort,
		"sub_path":              in.SubPath,
		"base_path":             in.BasePath,
		"sub_ips":               string(ipsJSON),
		"enable":                in.Enable,
		"allow_private_address": in.AllowPrivateAddress,
		"is_self":               in.IsSelf,
	}).Error
}

func (s *PeerService) Delete(id int) error {
	return database.GetDB().Where("id = ? AND role = ?", id, model.NodeRoleMaster).Delete(&model.Node{}).Error
}

func (s *PeerService) SetEnable(id int, enable bool) error {
	return database.GetDB().Model(model.Node{}).
		Where("id = ? AND role = ?", id, model.NodeRoleMaster).Update("enable", enable).Error
}

// Probe checks one peer's subscription server and reports what to persist. A
// peer whose published key equals ours is this very panel, which must never
// advertise itself as its own fallback.
func (s *PeerService) Probe(ctx context.Context, client *http.Client, p *model.Node) PeerHealthPatch {
	patch := PeerHealthPatch{LastHeartbeat: time.Now().Unix(), IsSelf: p.IsSelf}
	res := cluster.Probe(ctx, client, cluster.ProbeTarget{
		Scheme:       p.Scheme,
		Domain:       p.SubDomain,
		Port:         p.SubPort,
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
	return database.GetDB().Model(model.Node{}).Where("id = ?", id).Updates(updates).Error
}

// LiveEndpoints returns the peers a client can currently be pointed at, ordered
// by measured latency so the closest fallback is offered first. Rows describing
// this panel are dropped unless includeSelf, which the payload's master list
// wants and the fallback headers do not.
func (s *PeerService) LiveEndpoints(includeSelf bool) ([]cluster.Endpoint, error) {
	db := database.GetDB()
	var peers []*model.Node
	q := db.Where("role = ?", model.NodeRoleMaster).
		Where("enable = ?", true).
		Where("status = ?", "online")
	if !includeSelf {
		q = q.Where("is_self = ?", false)
	}
	if err := q.Order("latency_ms asc").Order("id asc").Find(&peers).Error; err != nil {
		return nil, err
	}
	out := make([]cluster.Endpoint, 0, len(peers))
	for _, p := range peers {
		out = append(out, cluster.Endpoint{Domain: p.SubDomain, Ips: p.SubIps})
	}
	return out, nil
}
