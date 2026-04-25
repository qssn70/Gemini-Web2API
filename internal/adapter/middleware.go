package adapter

import (
	"gemini-web2api/internal/config"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := config.GetConfig()
		requiredKey := strings.TrimSpace(cfg.Server.APIKey)

		if requiredKey == "" {
			requiredKey = strings.TrimSpace(os.Getenv("PROXY_API_KEY"))
		}

		if requiredKey == "" {
			c.Next()
			return
		}

		queryKey := strings.TrimSpace(c.Query("key"))
		headerKey := strings.TrimSpace(c.GetHeader("x-goog-api-key"))
		authHeader := strings.TrimSpace(c.GetHeader("Authorization"))

		if queryKey != "" && queryKey == requiredKey {
			c.Next()
			return
		}
		if headerKey != "" && headerKey == requiredKey {
			c.Next()
			return
		}

		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				token := strings.TrimSpace(parts[1])
				if token == requiredKey {
					c.Next()
					return
				}
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API Key"})
				return
			}
			if queryKey == "" && headerKey == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid Authorization header format"})
				return
			}
		}

		if queryKey == "" && headerKey == "" && authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "API Key is missing"})
			return
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API Key"})
	}
}

func LoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		accountID, exists := c.Get("account_id")
		if exists {
			displayID, ok := accountID.(string)
			if !ok || displayID == "" {
				displayID = "default"
			}
			log.Printf("[Account '%s'] %s %s - %d - %v",
				displayID,
				c.Request.Method,
				c.Request.URL.Path,
				c.Writer.Status(),
				time.Since(start),
			)
		}
	}
}

func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Recovery] Panic recovered: %v", r)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{
						"message": "Internal server error",
						"type":    "server_error",
					},
				})
			}
		}()
		c.Next()
	}
}
