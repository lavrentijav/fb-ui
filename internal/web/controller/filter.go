package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type FilterController struct {
	filterService service.FilterService
	xrayService   service.XrayService
}

func NewFilterController(g *gin.RouterGroup) *FilterController {
	a := &FilterController{}
	a.initRouter(g)
	return a
}

func (a *FilterController) initRouter(g *gin.RouterGroup) {
	g.GET("/lists", a.lists)
	g.GET("/lists/get/:id", a.getList)
	g.POST("/lists/add", a.addList)
	g.POST("/lists/update/:id", a.updateList)
	g.POST("/lists/del/:id", a.delList)

	g.GET("/rules", a.rules)
	g.GET("/rules/get/:id", a.getRule)
	g.POST("/rules/add", a.addRule)
	g.POST("/rules/update/:id", a.updateRule)
	g.POST("/rules/del/:id", a.delRule)
	g.POST("/rules/setEnable/:id", a.setRuleEnable)
	g.POST("/rules/reorder", a.reorderRules)
}

func (a *FilterController) lists(c *gin.Context) {
	lists, err := a.filterService.Lists()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.list"), err)
		return
	}
	jsonObj(c, lists, nil)
}

func (a *FilterController) getList(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	list, err := a.filterService.ListById(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.obtain"), err)
		return
	}
	jsonObj(c, list, nil)
}

func (a *FilterController) addList(c *gin.Context) {
	list, ok := middleware.BindAndValidate[model.FilterList](c)
	if !ok {
		return
	}
	if err := a.filterService.CreateList(list); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.addList"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsgObj(c, I18nWeb(c, "pages.filters.toasts.addList"), list, nil)
}

func (a *FilterController) updateList(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	list, ok := middleware.BindAndValidate[model.FilterList](c)
	if !ok {
		return
	}
	if err := a.filterService.UpdateList(id, list); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateList"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateList"), nil)
}

func (a *FilterController) delList(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	if err := a.filterService.DeleteList(id); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.deleteList"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, I18nWeb(c, "pages.filters.toasts.deleteList"), nil)
}

func (a *FilterController) rules(c *gin.Context) {
	rules, err := a.filterService.Rules()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.list"), err)
		return
	}
	jsonObj(c, rules, nil)
}

func (a *FilterController) getRule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	rule, err := a.filterService.RuleById(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.obtain"), err)
		return
	}
	jsonObj(c, rule, nil)
}

func (a *FilterController) addRule(c *gin.Context) {
	rule, ok := middleware.BindAndValidate[model.FilterRule](c)
	if !ok {
		return
	}
	if err := a.filterService.CreateRule(rule); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.addRule"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsgObj(c, I18nWeb(c, "pages.filters.toasts.addRule"), rule, nil)
}

func (a *FilterController) updateRule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	rule, ok := middleware.BindAndValidate[model.FilterRule](c)
	if !ok {
		return
	}
	if err := a.filterService.UpdateRule(id, rule); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), nil)
}

func (a *FilterController) delRule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	if err := a.filterService.DeleteRule(id); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.deleteRule"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, I18nWeb(c, "pages.filters.toasts.deleteRule"), nil)
}

// reorderRules rewrites the order of a filter chain in one call: the layers are
// evaluated top to bottom, so their order is part of what they do.
func (a *FilterController) reorderRules(c *gin.Context) {
	body := struct {
		Ids []int `json:"ids" form:"ids"`
	}{}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), err)
		return
	}
	if err := a.filterService.ReorderRules(body.Ids); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), nil)
}

func (a *FilterController) setRuleEnable(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	body := struct {
		Enable bool `json:"enable" form:"enable"`
	}{}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), err)
		return
	}
	if err := a.filterService.SetRuleEnable(id, body.Enable); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, I18nWeb(c, "pages.filters.toasts.updateRule"), nil)
}
