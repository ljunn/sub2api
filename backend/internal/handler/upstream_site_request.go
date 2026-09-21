package handler

import (
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func readSiteAwareRequestBody(c *gin.Context) ([]byte, error) {
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err == nil {
		c.Request = c.Request.WithContext(service.WithSitePriceRequest(c.Request.Context(), body))
	}
	return body, err
}
