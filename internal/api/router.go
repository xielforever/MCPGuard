package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/xielforever/MCPGuard/internal/auth"
	"github.com/xielforever/MCPGuard/internal/rules"
	"github.com/xielforever/MCPGuard/internal/store"
)

func SetupRouter(store *store.Store, ruleEngine *rules.Engine, authService *auth.ConfirmService) *gin.Engine {
	r := gin.Default()

	// CORS middleware
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	api := r.Group("/api/v1")
	{
		api.GET("/dashboard", func(c *gin.Context) {
			stats, _ := store.GetLogStats()
			c.JSON(http.StatusOK, gin.H{"stats": stats})
		})

		rulesGrp := api.Group("/rules")
		{
			rulesGrp.GET("", func(c *gin.Context) {
				rules, err := store.ListRules()
				if err != nil {
					c.JSON(500, gin.H{"error": err.Error()})
					return
				}
				c.JSON(200, rules)
			})
			rulesGrp.POST("", func(c *gin.Context) {})
			rulesGrp.PUT("/:id", func(c *gin.Context) {})
			rulesGrp.DELETE("/:id", func(c *gin.Context) {})
		}

		api.GET("/logs", func(c *gin.Context) {
			logs, err := store.ListLogs(50, 0)
			if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, logs)
		})

		api.POST("/confirm/:id", func(c *gin.Context) {
			id := c.Param("id")
			var req struct {
				Code string `json:"code"`
			}
			if err := c.BindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": "invalid request"})
				return
			}
			
			if authService.VerifyCode(id, req.Code) {
				c.JSON(200, gin.H{"status": "confirmed"})
			} else {
				c.JSON(403, gin.H{"error": "invalid or expired code"})
			}
		})

		api.GET("/settings", func(c *gin.Context) {
			c.JSON(200, gin.H{"message": "settings"})
		})
		api.POST("/settings", func(c *gin.Context) {})
	}

	// Serve static files for frontend
	r.Static("/web", "./web/dist")

	return r
}
