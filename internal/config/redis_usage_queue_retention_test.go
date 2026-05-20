package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRedisUsageQueueRetentionDefaultAndClamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("WriteFile default config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig default: %v", err)
	}
	if cfg.RedisUsageQueueRetentionSeconds != 60 {
		t.Fatalf("default retention = %d, want 60", cfg.RedisUsageQueueRetentionSeconds)
	}

	if err := os.WriteFile(path, []byte("redis-usage-queue-retention-seconds: -1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile negative config: %v", err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig negative: %v", err)
	}
	if cfg.RedisUsageQueueRetentionSeconds != 60 {
		t.Fatalf("negative retention = %d, want 60", cfg.RedisUsageQueueRetentionSeconds)
	}

	if err := os.WriteFile(path, []byte("redis-usage-queue-retention-seconds: 7200\n"), 0o600); err != nil {
		t.Fatalf("WriteFile large config: %v", err)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig large: %v", err)
	}
	if cfg.RedisUsageQueueRetentionSeconds != 3600 {
		t.Fatalf("large retention = %d, want 3600", cfg.RedisUsageQueueRetentionSeconds)
	}
}
