package responses

import "github.com/MoeForge/moego/ojson"

func chatCacheField(usage ojson.Result, name string) ojson.Result {
	for _, parent := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if v := usage.Get(parent + "." + name); v.Exists() {
			return v
		}
	}
	return ojson.Result{}
}

func chatCacheWriteDetails(usage ojson.Result) map[string]int64 {
	var out map[string]int64
	for _, name := range []string{"ttl_30m_tokens", "ttl_12h_tokens"} {
		if v := chatCacheField(usage, "cache_write_tokens_details."+name); v.Type == ojson.Number {
			if out == nil {
				out = make(map[string]int64)
			}
			out[name] = v.Int()
		}
	}
	return out
}
