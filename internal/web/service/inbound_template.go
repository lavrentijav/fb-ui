package service

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// InboundTemplateService snapshots inbounds into templates and stamps them
// onto nodes through the normal inbound path, so remote nodes get them via
// the runtime exactly as a hand-made inbound would.
type InboundTemplateService struct {
	inbounds InboundService
	clients  ClientService
	xray     XrayService
}

// InboundTemplateFromInbounds is the "save these inbounds as a template" payload.
type InboundTemplateFromInbounds struct {
	Name       string `json:"name" example:"RU entry"`
	Remark     string `json:"remark" example:"4 cascade countries"`
	InboundIds []int  `json:"inboundIds" example:"[116,120,124,128]"`
}

// InboundTemplateApply names the panels to stamp a template onto; 0 is this one.
type InboundTemplateApply struct {
	NodeIds []int `json:"nodeIds" example:"[18,19]"`
}

// InboundTemplateResult is what happened on one panel.
type InboundTemplateResult struct {
	NodeId   int      `json:"nodeId" example:"18"`
	Created  []string `json:"created" example:"[\"n18-in-2053-tcp\"]"`
	Skipped  []string `json:"skipped" example:"[\"2083\"]"`
	Attached int      `json:"attached" example:"15"`
	Errors   []string `json:"errors" example:"[]"`
}

const (
	tplNodeDomain = "{{NODE_DOMAIN}}"
	tplNodeName   = "{{NODE_NAME}}"
)

func (s *InboundTemplateService) List() ([]*model.InboundTemplate, error) {
	var rows []*model.InboundTemplate
	return rows, database.GetDB().Order("name asc").Find(&rows).Error
}

func (s *InboundTemplateService) Get(id int) (*model.InboundTemplate, error) {
	row := &model.InboundTemplate{}
	return row, database.GetDB().Where("id = ?", id).First(row).Error
}

func (s *InboundTemplateService) Delete(id int) error {
	return database.GetDB().Where("id = ?", id).Delete(&model.InboundTemplate{}).Error
}

// panelIdentity is the address and name a template's placeholders take on a
// panel; 0 is this panel, known by the registry row flagged as itself.
func panelIdentity(nodeId int) (address, name string, err error) {
	n := &model.Node{}
	q := database.GetDB().Where("id = ?", nodeId)
	if nodeId == SelfPanelId {
		q = database.GetDB().Where("is_self = ?", true)
	}
	if e := q.First(n).Error; e != nil {
		if nodeId == SelfPanelId && database.IsNotFound(e) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("node %d: %w", nodeId, e)
	}
	return n.Address, n.Name, nil
}

// FromInbounds snapshots inbounds without their clients. The source node's
// own address becomes {{NODE_DOMAIN}}, so certificate paths and SNIs follow
// the node a copy lands on.
func (s *InboundTemplateService) FromInbounds(req *InboundTemplateFromInbounds) (*model.InboundTemplate, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(req.InboundIds) == 0 {
		return nil, common.NewError("a name and at least one inbound are required")
	}
	tpl := &model.InboundTemplate{Name: name, Remark: strings.TrimSpace(req.Remark)}
	for _, id := range req.InboundIds {
		ib := &model.Inbound{}
		if err := database.GetDB().Where("id = ?", id).First(ib).Error; err != nil {
			return nil, common.NewErrorf("inbound %d not found", id)
		}
		src := SelfPanelId
		if ib.NodeID != nil {
			src = *ib.NodeID
		}
		addr, _, err := panelIdentity(src)
		if err != nil {
			return nil, err
		}
		settings, err := stripClients(ib.Settings)
		if err != nil {
			return nil, common.NewErrorf("inbound %d: %v", id, err)
		}
		generic := func(v string) string {
			if addr == "" || net.ParseIP(addr) != nil {
				return v
			}
			return strings.ReplaceAll(v, addr, tplNodeDomain)
		}
		tpl.Items = append(tpl.Items, model.InboundTemplateItem{
			Remark: ib.Remark, Enable: ib.Enable, Protocol: string(ib.Protocol), Listen: ib.Listen, Port: ib.Port,
			Settings: generic(settings), StreamSettings: generic(ib.StreamSettings), Sniffing: ib.Sniffing,
			SubSortIndex: ib.SubSortIndex, ShareAddrStrategy: ib.ShareAddrStrategy, ShareAddr: generic(ib.ShareAddr),
			ClientsFromInboundId: ib.Id,
		})
	}
	if err := database.GetDB().Create(tpl).Error; err != nil {
		return nil, err
	}
	return tpl, nil
}

// stripClients empties the client list: clients are attached at apply time,
// from whatever the source inbound has by then.
func stripClients(settings string) (string, error) {
	if strings.TrimSpace(settings) == "" {
		return settings, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(settings), &m); err != nil {
		return "", err
	}
	if _, ok := m["clients"]; ok {
		m["clients"] = []any{}
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// Apply stamps the template onto every panel. An inbound already present on
// the same listen address and port is left alone, so applying twice is safe.
func (s *InboundTemplateService) Apply(id int, req *InboundTemplateApply, userId int) ([]InboundTemplateResult, error) {
	tpl, err := s.Get(id)
	if err != nil {
		return nil, common.NewErrorf("template %d not found", id)
	}
	if len(req.NodeIds) == 0 {
		return nil, common.NewError("pick at least one panel")
	}
	var out []InboundTemplateResult
	needRestart := false
	for _, nodeId := range req.NodeIds {
		res := InboundTemplateResult{NodeId: nodeId}
		addr, name, err := panelIdentity(nodeId)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			out = append(out, res)
			continue
		}
		subst := strings.NewReplacer(tplNodeDomain, addr, tplNodeName, name).Replace
		taken, err := s.takenEndpoints(nodeId)
		if err != nil {
			return nil, err
		}
		for _, it := range tpl.Items {
			key := endpointKey(it.Listen, it.Port)
			if taken[key] {
				res.Skipped = append(res.Skipped, key)
				continue
			}
			ib := &model.Inbound{
				UserId: userId, Remark: subst(it.Remark), Enable: it.Enable, Protocol: model.Protocol(it.Protocol),
				Listen: it.Listen, Port: it.Port, Settings: subst(it.Settings), StreamSettings: subst(it.StreamSettings),
				Sniffing: it.Sniffing, SubSortIndex: it.SubSortIndex, ShareAddrStrategy: it.ShareAddrStrategy, ShareAddr: subst(it.ShareAddr),
			}
			if nodeId != SelfPanelId {
				n := nodeId
				ib.NodeID = &n
			}
			created, restart, err := s.inbounds.AddInbound(ib)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", key, err))
				continue
			}
			needRestart = needRestart || restart
			taken[key] = true
			res.Created = append(res.Created, created.Tag)
			if it.ClientsFromInboundId > 0 {
				clients, err := s.clients.ListForInbound(nil, it.ClientsFromInboundId)
				if err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: clients: %v", key, err))
					continue
				}
				emails := make([]string, 0, len(clients))
				for _, c := range clients {
					emails = append(emails, c.Email)
				}
				if len(emails) == 0 {
					continue
				}
				r, restart, err := s.clients.BulkAttach(&s.inbounds, emails, []int{created.Id})
				if err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: attach: %v", key, err))
					continue
				}
				needRestart = needRestart || restart
				res.Attached += len(r.Attached)
				res.Errors = append(res.Errors, r.Errors...)
			}
		}
		out = append(out, res)
	}
	if needRestart {
		s.xray.SetToNeedRestart()
	}
	return out, nil
}

func endpointKey(listen string, port int) string {
	return net.JoinHostPort(listen, fmt.Sprint(port))
}

func (s *InboundTemplateService) takenEndpoints(nodeId int) (map[string]bool, error) {
	var rows []model.Inbound
	q := database.GetDB().Select("listen", "port")
	if nodeId == SelfPanelId {
		q = q.Where("node_id IS NULL OR node_id = 0")
	} else {
		q = q.Where("node_id = ?", nodeId)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[endpointKey(r.Listen, r.Port)] = true
	}
	return taken, nil
}
