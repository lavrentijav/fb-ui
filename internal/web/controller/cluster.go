package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type ClusterController struct {
	registry *service.PanelRegistryService
}

func NewClusterController(g *gin.RouterGroup) *ClusterController {
	a := &ClusterController{registry: service.NewPanelRegistry("")}
	a.initRouter(g)
	return a
}

func (a *ClusterController) initRouter(g *gin.RouterGroup) {
	g.GET("/panels", a.panels)
}

// ClusterView is who shares this database and who is in charge of it. The
// leader is named rather than left to be derived from the deadlines, so a
// panel with a skewed clock cannot draw a different answer than the others.
type ClusterView struct {
	Panels     []*model.Panel `json:"panels"`
	LeaderGuid string         `json:"leaderGuid" example:"7f3a1c02-..."`
	SelfGuid   string         `json:"selfGuid" example:"9b21ff40-..."`
}

func (a *ClusterController) panels(c *gin.Context) {
	panels, err := a.registry.All()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.cluster.toasts.list"), err)
		return
	}
	view := ClusterView{Panels: panels}
	if leader, ok := a.registry.Leader(); ok {
		view.LeaderGuid = leader.Guid
	}
	if self, err := a.registry.Self(); err == nil {
		view.SelfGuid = self.Guid
	}
	jsonObj(c, view, nil)
}
