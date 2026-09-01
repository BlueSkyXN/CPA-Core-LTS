package config

import "testing"

func TestSDKConfigEffectiveAPIRequestBodyMaxBytes(t *testing.T) {
	tests := []struct {
		name string
		cfg  *SDKConfig
		want int64
	}{
		{name: "nil", cfg: nil, want: DefaultAPIRequestBodyMaxBytes},
		{name: "omitted", cfg: &SDKConfig{}, want: DefaultAPIRequestBodyMaxBytes},
		{name: "negative", cfg: &SDKConfig{APIRequestBodyMaxBytes: -1}, want: DefaultAPIRequestBodyMaxBytes},
		{name: "configured", cfg: &SDKConfig{APIRequestBodyMaxBytes: 64 << 20}, want: 64 << 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveAPIRequestBodyMaxBytes(); got != tt.want {
				t.Fatalf("EffectiveAPIRequestBodyMaxBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseConfigBytesAPIRequestBodyMaxBytes(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("api-request-body-max-bytes: 67108864\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if cfg.APIRequestBodyMaxBytes != 64<<20 {
		t.Fatalf("APIRequestBodyMaxBytes = %d, want %d", cfg.APIRequestBodyMaxBytes, 64<<20)
	}
}
