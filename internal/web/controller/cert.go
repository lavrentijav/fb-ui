package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// CertController manages TLS certificates and the DNS zones used to issue them.
type CertController struct {
	certService service.CertificateService
	dnsService  service.DnsProviderService
}

func NewCertController(certs *gin.RouterGroup, dns *gin.RouterGroup) *CertController {
	a := &CertController{}
	a.initRouter(certs, dns)
	return a
}

func (a *CertController) initRouter(certs *gin.RouterGroup, dns *gin.RouterGroup) {
	certs.GET("/list", a.list)
	certs.GET("/get/:id", a.get)
	certs.POST("/add", a.add)
	certs.POST("/update/:id", a.update)
	certs.POST("/del/:id", a.del)
	certs.POST("/issue/:id", a.issue)
	certs.POST("/scan", a.scan)
	certs.POST("/apply/:id", a.apply)
	certs.GET("/account", a.account)
	certs.POST("/account", a.setAccount)

	dns.GET("/list", a.dnsList)
	dns.GET("/get/:id", a.dnsGet)
	dns.POST("/add", a.dnsAdd)
	dns.POST("/update/:id", a.dnsUpdate)
	dns.POST("/del/:id", a.dnsDel)
	dns.POST("/test", a.dnsTest)
}

func pathID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return 0, false
	}
	return id, true
}

func (a *CertController) list(c *gin.Context) {
	rows, err := a.certService.List()
	jsonObj(c, rows, err)
}

func (a *CertController) get(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	row, err := a.certService.GetById(id)
	jsonObj(c, row, err)
}

func (a *CertController) add(c *gin.Context) {
	req := &service.CertificateRequest{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), err)
		return
	}
	row, err := a.certService.Create(req)
	jsonMsgObj(c, I18nWeb(c, "pages.certs.toasts.save"), row, err)
}

func (a *CertController) update(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	req := &service.CertificateRequest{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), a.certService.Update(id, req))
}

func (a *CertController) del(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.delete"), a.certService.Delete(id))
}

func (a *CertController) issue(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.issueStarted"), a.certService.StartIssue(id))
}

func (a *CertController) scan(c *gin.Context) {
	n, err := a.certService.Scan()
	jsonMsgObj(c, I18nWeb(c, "pages.certs.toasts.scan"), gin.H{"added": n}, err)
}

func (a *CertController) apply(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req struct {
		Target string `json:"target" form:"target"`
	}
	if err := c.ShouldBind(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.apply"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.apply"), a.certService.ApplyToPanel(id, req.Target))
}

func (a *CertController) account(c *gin.Context) {
	view, err := a.certService.GetAccount()
	jsonObj(c, view, err)
}

func (a *CertController) setAccount(c *gin.Context) {
	req := &service.AcmeAccountRequest{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), a.certService.SetAccount(req))
}

func (a *CertController) dnsList(c *gin.Context) {
	rows, err := a.dnsService.List()
	jsonObj(c, rows, err)
}

func (a *CertController) dnsGet(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	row, err := a.dnsService.GetById(id)
	jsonObj(c, row, err)
}

func (a *CertController) dnsAdd(c *gin.Context) {
	req := &service.DnsProviderRequest{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), err)
		return
	}
	row, err := a.dnsService.Create(req)
	jsonMsgObj(c, I18nWeb(c, "pages.certs.toasts.save"), row, err)
}

func (a *CertController) dnsUpdate(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	req := &service.DnsProviderRequest{}
	if err := c.ShouldBind(req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.save"), a.dnsService.Update(id, req))
}

func (a *CertController) dnsDel(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.delete"), a.dnsService.Delete(id))
}

// dnsTest verifies a token before saving: id 0 tests the unsaved form alone,
// an id with a blank credential tests the stored one with the edited fields.
func (a *CertController) dnsTest(c *gin.Context) {
	var req struct {
		Id int `json:"id" form:"id"`
		service.DnsProviderRequest
	}
	if err := c.ShouldBind(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.certs.toasts.dnsTest"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.certs.toasts.dnsTest"), a.dnsService.Test(req.Id, &req.DnsProviderRequest))
}
