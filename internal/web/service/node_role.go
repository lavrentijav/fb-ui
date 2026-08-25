package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// NodeRoleChangeRequest carries only what the *target* role needs. Fields of
// the role being left stay on the row, so flipping back does not re-ask them.
type NodeRoleChangeRequest struct {
	Role     string `json:"role" form:"role" validate:"required,oneof=master node" example:"master"`
	Scheme   string `json:"scheme" form:"scheme" validate:"omitempty,oneof=http https" example:"https"`
	BasePath string `json:"basePath" form:"basePath" example:"/"`
	// Omitted means keep what the row already has, the same as every other
	// field here; a bool and a slice need the nil to say so.
	AllowPrivateAddress *bool `json:"allowPrivateAddress" form:"allowPrivateAddress"`

	// Target role "node": how this panel reaches the node's panel API.
	Address          string `json:"address" form:"address" example:"node1.example.com"`
	Port             int    `json:"port" form:"port" validate:"omitempty,gte=1,lte=65535" example:"2053"`
	ApiToken         string `json:"apiToken" form:"apiToken" example:"abcdef0123456789"`
	TlsVerifyMode    string `json:"tlsVerifyMode" form:"tlsVerifyMode" validate:"omitempty,oneof=verify skip pin mtls" example:"verify"`
	PinnedCertSha256 string `json:"pinnedCertSha256" form:"pinnedCertSha256"`

	// Target role "master": how clients reach its subscription server.
	SubDomain string   `json:"subDomain" form:"subDomain" example:"sub2.example.com"`
	SubPort   int      `json:"subPort" form:"subPort" validate:"omitempty,gte=1,lte=65535" example:"2096"`
	SubPath   string   `json:"subPath" form:"subPath" example:"/sub/"`
	SubIps    []string `json:"subIps" form:"subIps"`
}

// SetRole flips a registered panel between the two things it can be to this
// one: a node it controls, or a master it falls back to. One row either way.
func (s *NodeService) SetRole(id int, req *NodeRoleChangeRequest) error {
	if req == nil {
		return common.NewError("role change request is required")
	}
	role := strings.TrimSpace(req.Role)
	if role != model.NodeRoleNode && role != model.NodeRoleMaster {
		return common.NewError("role must be node or master")
	}
	db := database.GetDB()
	existing := &model.Node{}
	if err := db.Where("id = ?", id).First(existing).Error; err != nil {
		return err
	}
	if currentRole(existing) == role {
		return common.NewError("panel is already registered as a " + role)
	}

	var updates map[string]any
	var err error
	if role == model.NodeRoleMaster {
		updates, err = s.toMasterUpdates(existing, req)
	} else {
		updates, err = s.toNodeUpdates(existing, req)
	}
	if err != nil {
		return err
	}
	// The two roles are watched by different jobs and "online" means a different
	// thing to each, so an observation made under the old role cannot survive.
	for column, value := range observedStateReset() {
		updates[column] = value
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(model.Node{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		// Runs after the reset above cleared the dirty flag, so the new node is
		// queued for its first config push.
		if role == model.NodeRoleNode {
			return s.MarkNodeDirtyTx(tx, id)
		}
		return nil
	}); err != nil {
		return err
	}
	if mgr := runtime.GetManager(); mgr != nil {
		mgr.InvalidateNode(id)
	}
	return nil
}

func currentRole(n *model.Node) string {
	if n.Role == model.NodeRoleMaster {
		return model.NodeRoleMaster
	}
	return model.NodeRoleNode
}

func (s *NodeService) toMasterUpdates(existing *model.Node, req *NodeRoleChangeRequest) (map[string]any, error) {
	// Same reason Delete refuses: those inbounds keep a node_id the node jobs no
	// longer visit, so they would silently stop being synced anywhere.
	var attached int64
	if err := database.GetDB().Model(&model.Inbound{}).Where("node_id = ?", existing.Id).Count(&attached).Error; err != nil {
		return nil, err
	}
	if attached > 0 {
		return nil, common.NewError(fmt.Sprintf("cannot turn this node into a master: %d inbound(s) still attached to it; move or delete them first", attached))
	}

	p := *existing
	p.Scheme = firstNonBlank(req.Scheme, existing.Scheme)
	p.SubDomain = firstNonBlank(req.SubDomain, existing.SubDomain, existing.Address)
	p.SubPath = firstNonBlank(req.SubPath, existing.SubPath)
	p.BasePath = firstNonBlank(req.BasePath, existing.BasePath)
	p.SubIps = req.SubIps
	if req.SubIps == nil {
		p.SubIps = existing.SubIps
	}
	p.AllowPrivateAddress = keepBool(req.AllowPrivateAddress, existing.AllowPrivateAddress)
	p.SubPort = req.SubPort
	if p.SubPort == 0 {
		p.SubPort = existing.SubPort
	}
	if err := (&PeerService{}).normalize(&p); err != nil {
		return nil, err
	}
	ipsJSON, err := json.Marshal(p.SubIps)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"role":                  model.NodeRoleMaster,
		"scheme":                p.Scheme,
		"address":               p.Address,
		"sub_domain":            p.SubDomain,
		"sub_port":              p.SubPort,
		"sub_path":              p.SubPath,
		"base_path":             p.BasePath,
		"sub_ips":               string(ipsJSON),
		"allow_private_address": p.AllowPrivateAddress,
		// The stored credential only ever opened the node API this row no longer
		// answers on; keeping it would leave a secret nothing can use.
		"api_token":  "",
		"is_self":    false,
		"public_key": "",
	}, nil
}

func (s *NodeService) toNodeUpdates(existing *model.Node, req *NodeRoleChangeRequest) (map[string]any, error) {
	if existing.IsSelf {
		return nil, common.NewError("this row is this panel itself and cannot be managed as a node")
	}
	n := *existing
	n.Role = model.NodeRoleNode
	n.Scheme = firstNonBlank(req.Scheme, existing.Scheme)
	n.Address = firstNonBlank(req.Address, existing.SubDomain, existing.Address)
	n.BasePath = firstNonBlank(req.BasePath, existing.BasePath)
	n.TlsVerifyMode = firstNonBlank(req.TlsVerifyMode, existing.TlsVerifyMode)
	n.PinnedCertSha256 = firstNonBlank(req.PinnedCertSha256, existing.PinnedCertSha256)
	n.AllowPrivateAddress = keepBool(req.AllowPrivateAddress, existing.AllowPrivateAddress)
	n.ApiToken = strings.TrimSpace(req.ApiToken)
	n.Port = req.Port
	if n.Port == 0 {
		n.Port = existing.Port
	}
	if err := s.normalize(&n); err != nil {
		return nil, err
	}
	if n.ApiToken == "" && n.TlsVerifyMode != "mtls" {
		return nil, common.NewError("apiToken is required unless mtls is enabled")
	}
	token := ""
	if n.ApiToken != "" {
		enc, err := nodetoken.Encrypt(existing.Id, n.ApiToken)
		if err != nil {
			return nil, err
		}
		token = enc
	}
	return map[string]any{
		"role":                  model.NodeRoleNode,
		"scheme":                n.Scheme,
		"address":               n.Address,
		"port":                  n.Port,
		"base_path":             n.BasePath,
		"api_token":             token,
		"tls_verify_mode":       n.TlsVerifyMode,
		"pinned_cert_sha256":    n.PinnedCertSha256,
		"allow_private_address": n.AllowPrivateAddress,
		// Master-only identity: relearned by the health job if it ever goes back.
		"is_self":    false,
		"public_key": "",
	}, nil
}

func observedStateReset() map[string]any {
	return map[string]any{
		"status":              "unknown",
		"last_heartbeat":      0,
		"latency_ms":          0,
		"last_error":          "",
		"xray_version":        "",
		"panel_version":       "",
		"cpu_pct":             0,
		"mem_pct":             0,
		"uptime_secs":         0,
		"net_up":              0,
		"net_down":            0,
		"xray_state":          "",
		"xray_error":          "",
		"guid":                "",
		"config_dirty":        false,
		"config_dirty_at":     0,
		"inbounds_adopted_at": 0,
	}
}

func keepBool(want *bool, current bool) bool {
	if want == nil {
		return current
	}
	return *want
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
