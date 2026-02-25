package http

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/engine"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

// Server implements the HTTP REST API
type Server struct {
	router      *gin.Engine
	userDB      *sqlite.UserDB
	activeDB    *sqlite.ActiveDB
	quotaEngine *engine.QuotaEngine
	logger      *zap.Logger
	secret      string
}

// NewServer creates a new HTTP server
func NewServer(
	userDB *sqlite.UserDB,
	activeDB *sqlite.ActiveDB,
	quotaEngine *engine.QuotaEngine,
	logger *zap.Logger,
	secret string,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(corsMiddleware())

	s := &Server{
		router:      router,
		userDB:      userDB,
		activeDB:    activeDB,
		quotaEngine: quotaEngine,
		logger:      logger,
		secret:      secret,
	}

	// Setup routes
	s.setupRoutes()

	return router
}

func (s *Server) setupRoutes() {
	// Health check (no auth required)
	s.router.GET("/health", s.healthCheck)

	// API v1 routes with auth
	api := s.router.Group("/api/v1")
	api.Use(s.authMiddleware())
	{
		// User routes
		api.GET("/users", s.listUsers)
		api.POST("/users", s.createUser)
		api.GET("/users/:id", s.getUser)
		api.PUT("/users/:id", s.updateUser)
		api.DELETE("/users/:id", s.deleteUser)

		// Package routes
		api.POST("/packages", s.createPackage)
		api.GET("/packages/:id", s.getPackage)
		api.GET("/users/:id/package", s.getUserPackage)

		// Node routes
		api.GET("/nodes", s.listNodes)
		api.POST("/nodes", s.createNode)
		api.GET("/nodes/:id", s.getNode)
		api.DELETE("/nodes/:id", s.deleteNode)

		// Service routes
		api.POST("/services", s.createService)
		api.GET("/services/:id", s.getService)
		api.DELETE("/services/:id", s.deleteService)

		// Stats routes
		api.GET("/stats", s.getStats)
	}
}

// Middleware

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		secret := c.Query("secret")
		if secret == "" {
			secret = c.GetHeader("X-Auth-Secret")
		}

		if secret == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		if secret == s.secret {
			c.Next()
			return
		}

		ok, err := s.userDB.ValidateOwnerAuthKey(secret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "auth validation failed"})
			c.Abort()
			return
		}
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// Health check

func (s *Server) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "healthy",
		"service": "hue",
		"version": "1.0.0",
	})
}

// User handlers

func (s *Server) listUsers(c *gin.Context) {
	filter := &domain.UserFilter{
		Limit:  100,
		Offset: 0,
	}

	if limit := c.Query("limit"); limit != "" {
		filter.Limit = parseInt(limit, 100)
	}
	if offset := c.Query("offset"); offset != "" {
		filter.Offset = parseInt(offset, 0)
	}
	if status := c.Query("status"); status != "" {
		s := domain.UserStatus(status)
		filter.Status = &s
	}
	if search := c.Query("search"); search != "" {
		filter.Search = &search
	}

	users, err := s.userDB.ListUsers(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"users": users,
		"total": len(users),
	})
}

func (s *Server) createUser(c *gin.Context) {
	var req domain.UserCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user := &domain.User{
		ID:             uuid.New().String(),
		ManagerID:      req.ManagerID,
		Username:       req.Username,
		Password:       req.Password,
		PublicKey:      req.PublicKey,
		PrivateKey:     req.PrivateKey,
		CACertList:     req.CACertList,
		Groups:         req.Groups,
		AllowedDevices: req.AllowedDevices,
		Status:         domain.UserStatusActive,
		ActivePackageID: req.ActivePackageID,
	}

	if err := s.userDB.CreateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, user)
}

func (s *Server) getUser(c *gin.Context) {
	id := c.Param("id")

	user, err := s.userDB.GetUser(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	c.JSON(http.StatusOK, user)
}

func (s *Server) updateUser(c *gin.Context) {
	id := c.Param("id")

	user, err := s.userDB.GetUser(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	var req domain.UserUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Update fields
	if req.Username != nil {
		user.Username = *req.Username
	}
	if req.ManagerID != nil {
		user.ManagerID = req.ManagerID
	}
	if req.Password != nil {
		user.Password = *req.Password
	}
	if req.PublicKey != nil {
		user.PublicKey = *req.PublicKey
	}
	if req.PrivateKey != nil {
		user.PrivateKey = *req.PrivateKey
	}
	if req.CACertList != nil {
		user.CACertList = *req.CACertList
	}
	if req.Groups != nil {
		user.Groups = *req.Groups
	}
	if req.AllowedDevices != nil {
		user.AllowedDevices = *req.AllowedDevices
	}
	if req.Status != nil {
		user.Status = *req.Status
	}
	if req.ActivePackageID != nil {
		user.ActivePackageID = req.ActivePackageID
	}

	if err := s.userDB.UpdateUser(user); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, user)
}

func (s *Server) deleteUser(c *gin.Context) {
	id := c.Param("id")

	if err := s.userDB.DeleteUser(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user deleted"})
}

// Package handlers

func (s *Server) createPackage(c *gin.Context) {
	var req domain.PackageCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	pkg := &domain.Package{
		ID:            uuid.New().String(),
		UserID:        req.UserID,
		TotalTraffic:  req.TotalTraffic,
		UploadLimit:   req.UploadLimit,
		DownloadLimit: req.DownloadLimit,
		ResetMode:     req.ResetMode,
		Duration:      req.Duration,
		StartAt:       req.StartAt,
		MaxConcurrent: req.MaxConcurrent,
		Status:        domain.PackageStatusActive,
	}

	if err := s.userDB.CreatePackage(pkg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, pkg)
}

func (s *Server) getPackage(c *gin.Context) {
	id := c.Param("id")

	pkg, err := s.userDB.GetPackage(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if pkg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "package not found"})
		return
	}

	c.JSON(http.StatusOK, pkg)
}

func (s *Server) getUserPackage(c *gin.Context) {
	userID := c.Param("id")

	pkg, err := s.userDB.GetPackageByUserID(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if pkg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "package not found"})
		return
	}

	c.JSON(http.StatusOK, pkg)
}

// Node handlers

func (s *Server) listNodes(c *gin.Context) {
	nodes, err := s.userDB.ListNodes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"nodes": nodes,
		"total": len(nodes),
	})
}

func (s *Server) createNode(c *gin.Context) {
	var req domain.NodeCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	node := &domain.Node{
		ID:                uuid.New().String(),
		SecretKey:         req.SecretKey,
		Name:              req.Name,
		AllowedIPs:        req.AllowedIPs,
		TrafficMultiplier: req.TrafficMultiplier,
		ResetMode:         req.ResetMode,
		ResetDay:          req.ResetDay,
		Country:           req.Country,
		City:              req.City,
		ISP:               req.ISP,
	}

	if err := s.userDB.CreateNode(node); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, node)
}

func (s *Server) getNode(c *gin.Context) {
	id := c.Param("id")

	node, err := s.userDB.GetNode(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if node == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	c.JSON(http.StatusOK, node)
}

func (s *Server) deleteNode(c *gin.Context) {
	id := c.Param("id")

	if err := s.userDB.DeleteNode(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "node deleted"})
}

// Service handlers

func (s *Server) createService(c *gin.Context) {
	var req domain.ServiceCreate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	authMethods := make([]domain.AuthMethod, len(req.AllowedAuthMethods))
	for i, m := range req.AllowedAuthMethods {
		authMethods[i] = m
	}

	service := &domain.Service{
		ID:                uuid.New().String(),
		SecretKey:         req.SecretKey,
		NodeID:            req.NodeID,
		Name:              req.Name,
		Protocol:          req.Protocol,
		AllowedAuthMethods: authMethods,
		CallbackURL:       req.CallbackURL,
	}

	if err := s.userDB.CreateService(service); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, service)
}

func (s *Server) getService(c *gin.Context) {
	id := c.Param("id")

	service, err := s.userDB.GetService(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if service == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "service not found"})
		return
	}

	c.JSON(http.StatusOK, service)
}

func (s *Server) deleteService(c *gin.Context) {
	id := c.Param("id")

	if err := s.userDB.DeleteService(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "service deleted"})
}

// Stats handler

func (s *Server) getStats(c *gin.Context) {
	users, _ := s.userDB.ListUsers(&domain.UserFilter{Limit: 1})
	nodes, _ := s.userDB.ListNodes()

	activeUsers := 0
	for _, u := range users {
		if u.Status == domain.UserStatusActive {
			activeUsers++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total_users":   len(users),
		"active_users":  activeUsers,
		"total_nodes":   len(nodes),
	})
}

// Helper functions

func parseInt(s string, defaultVal int) int {
	var val int
	if _, err := fmt.Sscanf(s, "%d", &val); err != nil {
		return defaultVal
	}
	return val
}
