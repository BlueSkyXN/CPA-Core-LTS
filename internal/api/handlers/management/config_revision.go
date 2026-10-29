package management

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func configRevision(data []byte) string {
	return fmt.Sprintf(`"%x"`, sha256.Sum256(data))
}

// The caller holds h.mu from the persisted read through the configuration write.
func checkConfigRevision(c *gin.Context, data []byte, required bool) bool {
	current := configRevision(data)
	c.Header("ETag", current)
	expected := strings.TrimSpace(c.GetHeader("If-Match"))
	if expected == "" {
		if !required {
			return true
		}
		c.JSON(http.StatusPreconditionRequired, gin.H{"error": "config_revision_required", "message": "Read the configuration and send its ETag in If-Match before saving."})
		return false
	}
	// A wildcard or a list cannot identify the single snapshot the editor changed.
	if expected != current || len(c.Request.Header.Values("If-Match")) != 1 {
		c.JSON(http.StatusPreconditionFailed, gin.H{"error": "config_revision_conflict", "message": "Configuration changed; reload and review your changes before saving."})
		return false
	}
	return true
}
