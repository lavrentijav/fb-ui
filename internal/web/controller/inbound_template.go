package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

// InboundTemplateController saves sets of inbounds and stamps them onto nodes.
type InboundTemplateController struct {
	templates service.InboundTemplateService
}

func NewInboundTemplateController(g *gin.RouterGroup) *InboundTemplateController {
	a := &InboundTemplateController{}
	g.GET("/list", a.list)
	g.POST("/fromInbounds", a.fromInbounds)
	g.POST("/apply/:id", a.apply)
	g.POST("/del/:id", a.del)
	return a
}

func (a *InboundTemplateController) list(c *gin.Context) {
	rows, err := a.templates.List()
	jsonObj(c, rows, err)
}

func (a *InboundTemplateController) fromInbounds(c *gin.Context) {
	req := &service.InboundTemplateFromInbounds{}
	if err := c.ShouldBindJSON(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.templates.toasts.save"), err)
		return
	}
	row, err := a.templates.FromInbounds(req)
	jsonMsgObj(c, I18nWeb(c, "pages.templates.toasts.save"), row, err)
}

func (a *InboundTemplateController) apply(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	req := &service.InboundTemplateApply{}
	if err := c.ShouldBindJSON(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.templates.toasts.apply"), err)
		return
	}
	userId := 0
	if u := session.GetLoginUser(c); u != nil {
		userId = u.Id
	}
	res, err := a.templates.Apply(id, req, userId)
	jsonMsgObj(c, I18nWeb(c, "pages.templates.toasts.apply"), res, err)
}

func (a *InboundTemplateController) del(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.templates.toasts.delete"), a.templates.Delete(id))
}
