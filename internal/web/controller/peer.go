package controller

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/cluster"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const peerProbeTimeout = 6 * time.Second

type PeerController struct {
	peerService    service.PeerService
	settingService service.SettingService
}

func NewPeerController(g *gin.RouterGroup) *PeerController {
	a := &PeerController{}
	a.initRouter(g)
	return a
}

func (a *PeerController) initRouter(g *gin.RouterGroup) {
	g.GET("/list", a.list)
	g.GET("/identity", a.identity)
	g.GET("/get/:id", a.get)

	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/del/:id", a.del)
	g.POST("/setEnable/:id", a.setEnable)
	g.POST("/probe/:id", a.probe)
}

func (a *PeerController) list(c *gin.Context) {
	peers, err := a.peerService.GetAll()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.list"), err)
		return
	}
	jsonObj(c, peers, nil)
}

// identity returns this panel's own subscription signing key so the operator can
// recognise it in a peer list, and clients can be given it out of band.
func (a *PeerController) identity(c *gin.Context) {
	_, public, err := a.settingService.SubSignKeypair()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.obtain"), err)
		return
	}
	jsonObj(c, cluster.Identity{Alg: cluster.SignatureAlgorithm, PublicKey: public}, nil)
}

func (a *PeerController) get(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	peer, err := a.peerService.GetById(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.obtain"), err)
		return
	}
	jsonObj(c, peer, nil)
}

func (a *PeerController) add(c *gin.Context) {
	peer, ok := middleware.BindAndValidate[model.Node](c)
	if !ok {
		return
	}
	if err := a.peerService.Create(peer); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.add"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.peers.toasts.add"), peer, nil)
}

func (a *PeerController) update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	peer, ok := middleware.BindAndValidate[model.Node](c)
	if !ok {
		return
	}
	if err := a.peerService.Update(id, peer); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.update"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.peers.toasts.update"), nil)
}

func (a *PeerController) del(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	if err := a.peerService.Delete(id); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.delete"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.peers.toasts.delete"), nil)
}

func (a *PeerController) setEnable(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	body := struct {
		Enable bool `json:"enable" form:"enable"`
	}{}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.update"), err)
		return
	}
	if err := a.peerService.SetEnable(id, body.Enable); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.update"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.peers.toasts.update"), nil)
}

// probe checks one peer on demand and persists the result, so the operator does
// not have to wait for the next health tick after fixing an address.
func (a *PeerController) probe(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	peer, err := a.peerService.GetById(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.obtain"), err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), peerProbeTimeout)
	defer cancel()
	patch := a.peerService.Probe(ctx, cluster.NewProbeClient(), peer)
	if err := a.peerService.UpdateHealth(id, patch); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.peers.toasts.update"), err)
		return
	}
	jsonObj(c, patch, nil)
}
