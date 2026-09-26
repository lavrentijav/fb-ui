package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ProvisionController turns bare servers into nodes over SSH.
type ProvisionController struct {
	provisionService service.NodeProvisionService
}

func NewProvisionController(g *gin.RouterGroup) *ProvisionController {
	a := &ProvisionController{}
	g.POST("/start", a.start)
	g.GET("/list", a.list)
	g.GET("/get/:id", a.get)
	return a
}

func (a *ProvisionController) start(c *gin.Context) {
	req := &service.NodeProvisionRequest{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.provision.toasts.start"), err)
		return
	}
	job, err := a.provisionService.Start(req)
	jsonMsgObj(c, I18nWeb(c, "pages.provision.toasts.start"), job, err)
}

func (a *ProvisionController) list(c *gin.Context) {
	jsonObj(c, a.provisionService.List(), nil)
}

func (a *ProvisionController) get(c *gin.Context) {
	job, err := a.provisionService.Get(c.Param("id"))
	jsonObj(c, job, err)
}
