package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// EdgeController manages the shared public port: SNI and HTTP routes.
type EdgeController struct {
	edgeService service.EdgeService
}

func NewEdgeController(g *gin.RouterGroup) *EdgeController {
	a := &EdgeController{}
	g.GET("/settings", a.settings)
	g.POST("/settings", a.setSettings)
	g.GET("/routes", a.routes)
	g.POST("/routes/save", a.saveRoute)
	g.POST("/routes/del/:id", a.delRoute)
	g.GET("/status", a.status)
	g.POST("/check", a.check)
	g.GET("/maintenance", a.maintenance)
	g.POST("/maintenance", a.setMaintenance)
	return a
}

func (a *EdgeController) settings(c *gin.Context) {
	v, err := a.edgeService.GetSettings()
	jsonObj(c, v, err)
}

func (a *EdgeController) setSettings(c *gin.Context) {
	req := &service.EdgeSettings{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.edge.toasts.save"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.edge.toasts.save"), a.edgeService.SetSettings(req))
}

func (a *EdgeController) routes(c *gin.Context) {
	rows, err := a.edgeService.List()
	jsonObj(c, rows, err)
}

func (a *EdgeController) saveRoute(c *gin.Context) {
	req := &model.EdgeRoute{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.edge.toasts.save"), err)
		return
	}
	row, err := a.edgeService.Save(req)
	jsonMsgObj(c, I18nWeb(c, "pages.edge.toasts.save"), row, err)
}

func (a *EdgeController) delRoute(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.edge.toasts.delete"), a.edgeService.Delete(id))
}

func (a *EdgeController) status(c *gin.Context) {
	jsonObj(c, a.edgeService.Status(), nil)
}

func (a *EdgeController) check(c *gin.Context) {
	res, err := a.edgeService.CheckTargets()
	jsonObj(c, res, err)
}

func (a *EdgeController) maintenance(c *gin.Context) {
	jsonObj(c, service.MaintenanceState(), nil)
}

func (a *EdgeController) setMaintenance(c *gin.Context) {
	req := &service.MaintenanceRequest{}
	if err := c.ShouldBindJSON(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.edge.toasts.save"), err)
		return
	}
	st, err := service.SetMaintenance(*req)
	jsonMsgObj(c, I18nWeb(c, "pages.edge.toasts.save"), st, err)
}
