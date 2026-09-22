package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type UpstreamSiteHandler struct{ service *service.UpstreamSiteService }

func NewUpstreamSiteHandler(s *service.UpstreamSiteService) *UpstreamSiteHandler {
	return &UpstreamSiteHandler{service: s}
}
func (h *UpstreamSiteHandler) List(c *gin.Context) {
	sites, err := h.service.List(c.Request.Context())
	if err != nil {
		response.InternalError(c, "读取站点失败")
		return
	}
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	if err := h.service.AnnotateUnreadModels(c.Request.Context(), sites, subject.UserID); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	for i := range sites {
		service.ApplySiteManualPrices(&sites[i])
		if err := h.service.AnnotateTrafficSupport(c.Request.Context(), &sites[i]); err != nil {
			response.InternalError(c, "读取扶持设置失败")
			return
		}
	}
	response.Success(c, sites)
}

func (h *UpstreamSiteHandler) respondSite(c *gin.Context, site *service.UpstreamSite) {
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	sites := []service.UpstreamSite{*site}
	if err := h.service.AnnotateUnreadModels(c.Request.Context(), sites, subject.UserID); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	service.ApplySiteManualPrices(&sites[0])
	if err := h.service.AnnotateTrafficSupport(c.Request.Context(), &sites[0]); err != nil {
		response.InternalError(c, "读取扶持设置失败")
		return
	}
	response.Success(c, sites[0])
}

func (h *UpstreamSiteHandler) MarkModelsRead(c *gin.Context) {
	var input struct {
		IDs []string `json:"discovery_ids"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "模型已读参数无效")
		return
	}
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	ids, err := h.service.MarkModelsRead(c.Request.Context(), c.Param("id"), subject.UserID, input.IDs)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, gin.H{"discovery_ids": ids})
}
func (h *UpstreamSiteHandler) Save(c *gin.Context) {
	var input service.SiteInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "站点参数无效")
		return
	}
	site, err := h.service.Save(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.respondSite(c, site)
}
func (h *UpstreamSiteHandler) Sync(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	site, err := h.service.Sync(ctx, c.Param("id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.respondSite(c, site)
}
func (h *UpstreamSiteHandler) Bind(c *gin.Context) {
	var input service.SiteBinding
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "绑定参数无效")
		return
	}
	input.ID = c.Param("binding_id")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	site, err := h.service.Bind(ctx, c.Param("id"), input)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.respondSite(c, site)
}
func (h *UpstreamSiteHandler) Unbind(c *gin.Context) {
	site, err := h.service.Unbind(c.Request.Context(), c.Param("id"), c.Param("binding_id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.respondSite(c, site)
}
func (h *UpstreamSiteHandler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("id")); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *UpstreamSiteHandler) PricingContext(c *gin.Context) {
	c.Request = c.Request.WithContext(h.service.WithPricingContext(c.Request.Context()))
	if c.Request.Method == http.MethodPost && !strings.HasPrefix(c.Request.URL.Path, "/api/") {
		ctx, finish := service.WithSiteTrafficRequest(c.Request.Context(), c.Request.URL.Path)
		defer finish()
		c.Request = c.Request.WithContext(ctx)
	}
	c.Next()
}

func (h *UpstreamSiteHandler) SaveTrafficSupport(c *gin.Context) {
	var input service.SiteTrafficSupport
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "流量扶持参数无效")
		return
	}
	if err := h.service.SaveTrafficSupport(c.Request.Context(), c.Param("id"), c.Param("binding_id"), input); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, input)
}
func (h *UpstreamSiteHandler) PricePreview(c *gin.Context) {
	var input service.SiteBinding
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "绑定参数无效")
		return
	}
	result, err := h.service.PricePreview(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *UpstreamSiteHandler) SaveManualPrice(c *gin.Context) {
	var input service.SiteManualPriceInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "采购价参数无效")
		return
	}
	site, err := h.service.SaveManualPrice(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	h.respondSite(c, site)
}
