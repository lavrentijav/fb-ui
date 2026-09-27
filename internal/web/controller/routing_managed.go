package controller

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ManagedRoutingController is the node side of the routing a master owns for
// this panel: filter and cascade rules plus the outbounds they target.
type ManagedRoutingController struct {
	xray service.XrayService
}

func NewManagedRoutingController(g *gin.RouterGroup) *ManagedRoutingController {
	a := &ManagedRoutingController{}
	g.GET("/managed", a.get)
	g.POST("/managed", a.set)
	return a
}

func (a *ManagedRoutingController) get(c *gin.Context) {
	mr, err := service.CurrentManagedRouting()
	jsonObj(c, mr, err)
}

func (a *ManagedRoutingController) set(c *gin.Context) {
	mr := &service.ManagedRouting{}
	if err := json.NewDecoder(c.Request.Body).Decode(mr); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	changed, err := a.xray.StoreManagedRouting(mr)
	jsonObj(c, gin.H{"changed": changed}, err)
}
