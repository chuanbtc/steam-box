package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"steam-box/internal/auth"
)

// AuthMiddleware reads a JWT from the Authorization header ("Bearer <token>")
// or the "admin_session" cookie. On success it sets "userID" and "role" in the
// gin context; on failure it aborts with 401.
func AuthMiddleware(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var tokenStr string

		// Try Authorization header first.
		if header := c.GetHeader("Authorization"); header != "" {
			parts := strings.SplitN(header, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenStr = parts[1]
			}
		}

		// Fall back to admin_session cookie.
		if tokenStr == "" {
			if cookie, err := c.Cookie("admin_session"); err == nil && cookie != "" {
				tokenStr = cookie
			}
		}

		if tokenStr == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authentication token"})
			return
		}

		userID, role, err := auth.ParseToken(tokenStr, jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set("userID", userID)
		c.Set("role", role)
		c.Next()
	}
}

// AdminOnly aborts with 403 unless the authenticated user has role "superadmin".
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") != "superadmin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "superadmin access required"})
			return
		}
		c.Next()
	}
}

// CORS returns a middleware that sets permissive CORS headers (dev mode).
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization")
		c.Header("Access-Control-Expose-Headers", "Content-Length")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// visitorRecord tracks request counts for a single IP within the current window.
type visitorRecord struct {
	mu       sync.Mutex
	count    int
	windowAt time.Time
}

// RateLimiter enforces a per-IP request cap of rpm requests per minute.
// Exceeding the limit returns 429 Too Many Requests.
func RateLimiter(rpm int) gin.HandlerFunc {
	var visitors sync.Map // map[string]*visitorRecord

	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()

		val, _ := visitors.LoadOrStore(ip, &visitorRecord{
			count:    0,
			windowAt: now,
		})
		rec := val.(*visitorRecord)

		rec.mu.Lock()
		// Reset window if a minute has elapsed.
		if now.Sub(rec.windowAt) >= time.Minute {
			rec.count = 0
			rec.windowAt = now
		}
		rec.count++
		exceeded := rec.count > rpm
		rec.mu.Unlock()

		if exceeded {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}

		c.Next()
	}
}
