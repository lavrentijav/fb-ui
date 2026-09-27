package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// DomainController moves or copies every name under one base domain to another.
type DomainController struct {
	migrate service.DomainMigrateService
}

func NewDomainController(g *gin.RouterGroup) *DomainController {
	a := &DomainController{}
	g.POST("/plan", a.plan)
	g.POST("/apply", a.apply)
	return a
}

func (a *DomainController) plan(c *gin.Context) {
	req := &service.DomainMigrateRequest{}
	if err := c.ShouldBindJSON(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.domains.title"), err)
		return
	}
	actions, err := a.migrate.Plan(req)
	jsonObj(c, actions, err)
}

func (a *DomainController) apply(c *gin.Context) {
	req := &service.DomainMigrateRequest{}
	if err := c.ShouldBindJSON(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.domains.title"), err)
		return
	}
	actions, err := a.migrate.Apply(req)
	jsonMsgObj(c, I18nWeb(c, "pages.domains.title"), actions, err)
}
