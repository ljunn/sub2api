package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
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
	response.Success(c, sites)
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
	response.Success(c, site)
}
func (h *UpstreamSiteHandler) Sync(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	site, err := h.service.Sync(ctx, c.Param("id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, site)
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
	response.Success(c, site)
}
func (h *UpstreamSiteHandler) Unbind(c *gin.Context) {
	site, err := h.service.Unbind(c.Request.Context(), c.Param("id"), c.Param("binding_id"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, site)
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
	c.Next()
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
