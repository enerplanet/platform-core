package middleware

import "github.com/gin-gonic/gin"

// InjectUserContext populates gin context keys from forwarded headers so that
// downstream handlers can reuse httputil helpers.
func InjectUserContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		setIfNonEmpty(c, "user_id", c.GetHeader("X-User-ID"))
		setIfNonEmpty(c, "user_email", c.GetHeader("X-User-Email"))
		setIfNonEmpty(c, "user_name", c.GetHeader("X-User-Name"))
		setIfNonEmpty(c, "access_level", c.GetHeader("X-Access-Level"))
		setIfNonEmpty(c, "group_id", c.GetHeader("X-Group-ID"))
		c.Next()
	}
}

func setIfNonEmpty(c *gin.Context, key, value string) {
	if value != "" {
		c.Set(key, value)
	}
}
