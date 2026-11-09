package claudecommon

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// ValidBigModelWebReaderResult 校验 webReader 返回的 JSON 数组字符串。
// 每页保留完整对象；额外的标题、元数据等字段由转换器原样回放。
func ValidBigModelWebReaderResult(content string) bool {
	if !ojson.Valid(content) {
		return false
	}
	pages := ojson.Parse(content)
	if !pages.IsArray() {
		return false
	}
	for _, page := range pages.Array() {
		if !page.IsObject() || page.Get("url").Type != ojson.String || strings.TrimSpace(page.Get("url").String()) == "" || page.Get("content").Type != ojson.String {
			return false
		}
	}
	return true
}
