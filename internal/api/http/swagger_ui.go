package http

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed swagger/index.html
var swaggerIndexHTML []byte

func (s *Server) swaggerUI(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", swaggerIndexHTML)
}
