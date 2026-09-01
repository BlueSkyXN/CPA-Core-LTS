package config

const DefaultAPIRequestBodyMaxBytes int64 = 16 << 20

// EffectiveAPIRequestBodyMaxBytes returns the configured public API request
// body limit, preserving the bounded default for omitted or invalid values.
func (c *SDKConfig) EffectiveAPIRequestBodyMaxBytes() int64 {
	if c == nil || c.APIRequestBodyMaxBytes <= 0 {
		return DefaultAPIRequestBodyMaxBytes
	}
	return c.APIRequestBodyMaxBytes
}
