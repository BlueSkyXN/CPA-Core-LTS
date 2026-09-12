package usage

import (
	"net/url"
	"strings"
)

// SafeBaseURL 保留端点定位信息，不传播 URL 内的凭据、查询参数或 fragment。
func SafeBaseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}
