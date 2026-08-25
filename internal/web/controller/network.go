package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type NetworkController struct {
	networkService service.NetworkService
}

func NewNetworkController(g *gin.RouterGroup) *NetworkController {
	a := &NetworkController{}
	a.initRouter(g)
	return a
}

func (a *NetworkController) initRouter(g *gin.RouterGroup) {
	g.GET("/graph", a.graph)

	g.POST("/link", a.addLink)
	g.POST("/link/del/:id", a.delLink)
	g.POST("/link/setEnable/:id", a.setLinkEnable)
}

func (a *NetworkController) graph(c *gin.Context) {
	graph, err := a.networkService.Graph()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.network.toasts.graph"), err)
		return
	}
	jsonObj(c, graph, nil)
}

func (a *NetworkController) addLink(c *gin.Context) {
	link, ok := middleware.BindAndValidate[model.CascadeLink](c)
	if !ok {
		return
	}
	if err := a.networkService.AddLink(link); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.network.toasts.link"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.network.toasts.link"), link, nil)
}

func (a *NetworkController) delLink(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	if err := a.networkService.DeleteLink(id); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.network.toasts.unlink"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.network.toasts.unlink"), nil)
}

func (a *NetworkController) setLinkEnable(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	body := struct {
		Enable bool `json:"enable" form:"enable"`
	}{}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.network.toasts.link"), err)
		return
	}
	if err := a.networkService.SetLinkEnable(id, body.Enable); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.network.toasts.link"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.network.toasts.link"), nil)
}
