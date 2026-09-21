package admin

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *UpstreamSiteHandler) Balance(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	site, err := h.service.RefreshBalance(ctx, c.Param("id"))
	if err != nil {
		response.BadRequest(c, "查询或保存站点余额失败")
		return
	}
	response.Success(c, site)
}

func (h *UpstreamSiteHandler) BalanceSettings(c *gin.Context) {
	settings, err := h.service.BalanceSettings(c.Request.Context())
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, settings)
}

func (h *UpstreamSiteHandler) SaveBalanceSettings(c *gin.Context) {
	var input service.SiteBalanceSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "余额提醒设置无效")
		return
	}
	settings, err := h.service.SaveBalanceSettings(c.Request.Context(), input)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, settings)
}
