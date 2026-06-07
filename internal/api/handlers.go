package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"steam-box/internal/auth"
	"steam-box/internal/cdk"
	"steam-box/internal/config"
	"steam-box/internal/db"
	"steam-box/internal/lua"
	"steam-box/internal/manifest"
	"steam-box/internal/middleware"
)

// Server is the top-level HTTP server holding all dependencies.
type Server struct {
	DB     *gorm.DB
	CDK    *cdk.Service
	Config *config.Config
	Router *gin.Engine
}

// NewServer wires up middleware, registers every route, and returns a ready
// Server. Call Run() to start listening.
func NewServer(database *gorm.DB, cdkSvc *cdk.Service, cfg *config.Config) *Server {
	r := gin.Default()
	r.Use(middleware.CORS())

	s := &Server{
		DB:     database,
		CDK:    cdkSvc,
		Config: cfg,
		Router: r,
	}

	// ── Public routes (no auth) ──────────────────────────────────────────
	r.GET("/", s.handleActivatePS1)
	r.GET("/hook", s.handleHookPS1)
	r.GET("/api/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "service": "steam-box", "depot_keys": manifest.DepotKeysCount()})
	})
	r.POST("/api/redeem", s.handleRedeem)
	r.POST("/api/login", s.handleLogin)
	r.GET("/api/lua-payload", s.handleLuaPayload)

	// ── Static files ─────────────────────────────────────────────────────
	r.Static("/static", "static")

	// ── Admin page (public, auth in JS) ─────────────────────────────────
	r.GET("/admin", s.handleAdminPage)

	// ── Admin API routes (JWT required) ──────────────────────────────────
	admin := r.Group("/", middleware.AuthMiddleware(cfg.JWTSecret))
	{
		admin.GET("/api/admin/dashboard", s.handleDashboard)
		admin.GET("/api/admin/game/lookup", s.handleGameLookup)
		admin.POST("/api/admin/cdk/generate", s.handleCDKGenerate)
		admin.GET("/api/admin/cdk/list", s.handleCDKList)
		admin.POST("/api/admin/cdk/revoke", s.handleCDKRevoke)
		admin.POST("/api/admin/depot-keys/reload", s.handleDepotKeysReload)
	}

	return s
}

// Run starts the HTTP server on the configured host:port.
func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.Config.Host, s.Config.Port)
	return s.Router.Run(addr)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func publicURL(c *gin.Context, cfg *config.Config) string {
	if cfg.PublicURL != "" {
		return strings.TrimRight(cfg.PublicURL, "/")
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request.Host)
}

func (s *Server) servePS1(c *gin.Context, filename string) {
	path := fmt.Sprintf("scripts/%s", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to read %s: %v", filename, err)
		return
	}
	base := publicURL(c, s.Config)
	body := strings.ReplaceAll(string(data), "__INJECT_API_BASE__", base)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(body))
}

// buildFullLua constructs the complete Lua payload for an AppID, including
// all depot keys and DLC unlocks discovered from upstream sources.
func buildFullLua(appID string) (string, string) {
	gameName := fmt.Sprintf("AppID %s", appID)
	if info, err := manifest.LookupGame(appID); err == nil {
		gameName = info.Name
	}

	depots := manifest.FindDepotsForApp(appID)
	dlcs := manifest.FindDLCsForApp(appID)

	g := &lua.GameLua{AppID: appID, Name: gameName, DLCs: dlcs}
	for _, d := range depots {
		g.Depots = append(g.Depots, lua.DepotInfo{
			DepotID:  d.DepotID,
			DepotKey: d.Key,
		})
	}

	return lua.GenerateLua(g), gameName
}

// ─── Public Handlers ─────────────────────────────────────────────────────────

// GET / — serve the activation bootstrap script.
func (s *Server) handleActivatePS1(c *gin.Context) {
	s.servePS1(c, "activate.ps1")
}

// GET /hook — serve the hook/loader script.
func (s *Server) handleHookPS1(c *gin.Context) {
	s.servePS1(c, "hook.ps1")
}

// POST /api/redeem
func (s *Server) handleRedeem(c *gin.Context) {
	var req struct {
		CDK     string `json:"cdk" binding:"required"`
		Machine string `json:"machine" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "message": "cdk and machine are required"})
		return
	}

	key, err := s.CDK.Redeem(req.CDK, req.Machine)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": err.Error()})
		return
	}

	// Build FULL lua with depot keys + DLC unlocks
	fullLua, gameName := buildFullLua(key.AppID)
	if key.GameName != "" {
		gameName = key.GameName
	}
	luaB64 := base64.StdEncoding.EncodeToString([]byte(fullLua))

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"appid":   key.AppID,
		"name":    gameName,
		"lua_b64": luaB64,
		"message": "激活成功",
	})
}

// POST /api/login
func (s *Server) handleLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "message": "username and password are required"})
		return
	}

	var user db.User
	if err := s.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "message": "invalid credentials"})
		return
	}

	if !user.Enabled {
		c.JSON(http.StatusForbidden, gin.H{"ok": false, "message": "account disabled"})
		return
	}

	if !auth.CheckPassword(req.Password, user.Salt, user.PasswordHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "message": "invalid credentials"})
		return
	}

	token, err := auth.GenerateToken(user.ID, user.Role, s.Config.JWTSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "message": "failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":    true,
		"token": token,
		"role":  user.Role,
	})
}

// GET /api/lua-payload?app=<appid>&t=<token>
func (s *Server) handleLuaPayload(c *gin.Context) {
	appID := c.Query("app")
	token := c.Query("t")

	if appID == "" || token == "" {
		c.String(http.StatusBadRequest, "-- missing app or t parameter")
		return
	}

	var logs []db.ActivationLog
	s.DB.Where("app_id = ? AND ok = ?", appID, true).Find(&logs)

	valid := false
	for _, l := range logs {
		expected := lua.GenerateToken(appID, l.Machine, s.Config.JWTSecret)
		if expected == token {
			valid = true
			break
		}
	}

	if !valid {
		c.String(http.StatusForbidden, "-- invalid token")
		return
	}

	payload, _ := buildFullLua(appID)
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(payload))
}

// ─── Admin Handlers ──────────────────────────────────────────────────────────

func (s *Server) handleAdminPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.File("static/admin.html")
}

// GET /api/admin/game/lookup?appid=730
func (s *Server) handleGameLookup(c *gin.Context) {
	appID := strings.TrimSpace(c.Query("appid"))
	if appID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "message": "appid is required"})
		return
	}

	info, err := manifest.LookupGame(appID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": err.Error()})
		return
	}

	depots := manifest.FindDepotsForApp(appID)
	depotList := make([]gin.H, 0, len(depots))
	for _, d := range depots {
		depotList = append(depotList, gin.H{"depot_id": d.DepotID, "key": d.Key})
	}

	// Build DLC info with names
	dlcList := make([]gin.H, 0, len(info.DLCs))
	for _, dlcID := range info.DLCs {
		dlcInfo := gin.H{"appid": dlcID, "name": fmt.Sprintf("DLC %s", dlcID)}
		if dlcGame, err := manifest.LookupGame(dlcID); err == nil {
			dlcInfo["name"] = dlcGame.Name
		}
		// Check if we have depot keys for this DLC
		dlcIDInt, _ := strconv.Atoi(dlcID)
		if dlcIDInt > 0 {
			dlcDepotKey := manifest.GetDepotKey(fmt.Sprintf("%d", dlcIDInt+1))
			if dlcDepotKey == "" {
				dlcDepotKey = manifest.GetDepotKey(dlcID)
			}
			dlcInfo["has_key"] = dlcDepotKey != ""
		}
		dlcList = append(dlcList, dlcInfo)
	}

	// Preview the generated Lua
	fullLua, _ := buildFullLua(appID)

	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"appid":       info.AppID,
		"name":        info.Name,
		"type":        info.Type,
		"header_url":  info.HeaderURL,
		"depots":      depotList,
		"depot_count": len(depotList),
		"dlcs":        dlcList,
		"dlc_count":   len(dlcList),
		"lua_preview": fullLua,
	})
}

// GET /api/admin/dashboard
func (s *Server) handleDashboard(c *gin.Context) {
	var totalCDKs int64
	var usedCDKs int64
	var unusedCDKs int64
	var totalUsers int64

	s.DB.Model(&db.CDKey{}).Count(&totalCDKs)
	s.DB.Model(&db.CDKey{}).Where("used = ?", true).Count(&usedCDKs)
	s.DB.Model(&db.CDKey{}).Where("used = ?", false).Count(&unusedCDKs)
	s.DB.Model(&db.User{}).Count(&totalUsers)

	c.JSON(http.StatusOK, gin.H{
		"total_cdks":      totalCDKs,
		"used_cdks":       usedCDKs,
		"unused_cdks":     unusedCDKs,
		"total_users":     totalUsers,
		"depot_keys_loaded": manifest.DepotKeysCount(),
	})
}

// POST /api/admin/cdk/generate
func (s *Server) handleCDKGenerate(c *gin.Context) {
	var req struct {
		AppID    string `json:"appid" binding:"required"`
		GameName string `json:"game_name"`
		Count    int    `json:"count" binding:"required"`
		Note     string `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "message": "appid and count are required"})
		return
	}

	gameName := strings.TrimSpace(req.GameName)
	if gameName == "" {
		if info, err := manifest.LookupGame(req.AppID); err == nil {
			gameName = info.Name
		} else {
			gameName = fmt.Sprintf("AppID %s", req.AppID)
		}
	}

	createdBy := c.GetString("userID")
	agentID := createdBy
	codes, err := s.CDK.Generate(req.AppID, gameName, agentID, createdBy, req.Note, req.Count)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":        true,
		"cdks":      codes,
		"game_name": gameName,
	})
}

// GET /api/admin/cdk/list?limit=50&filter=all
func (s *Server) handleCDKList(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "50")
	filter := c.DefaultQuery("filter", "all")

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 50
	}

	items, err := s.CDK.List("", limit, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":    true,
		"items": items,
	})
}

// POST /api/admin/cdk/revoke
func (s *Server) handleCDKRevoke(c *gin.Context) {
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "message": "code is required"})
		return
	}

	if err := s.CDK.Revoke(req.Code); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "message": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// POST /api/admin/depot-keys/reload — force re-fetch all depot key sources
func (s *Server) handleDepotKeysReload(c *gin.Context) {
	if err := manifest.ReloadDepotKeys(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"ok": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":    true,
		"count": manifest.DepotKeysCount(),
	})
}
