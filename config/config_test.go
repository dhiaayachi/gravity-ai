package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_Defaults(t *testing.T) {
	config, err := LoadConfig("", nil)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:8000", config.BindAddr)
	assert.Equal(t, ":8080", config.HTTPAddr)
	assert.Equal(t, "info", config.LogLevel)
	assert.Equal(t, "./data", config.DataDir)
}

func TestLoadConfig_ConfigFile(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gravity.yaml")
	configContent := []byte(`
addr: 127.0.0.1:9090
http_addr: :9091
log_level: debug
data_dir: /tmp/gravity
`)
	err := os.WriteFile(configFile, configContent, 0644)
	require.NoError(t, err)

	config, err := LoadConfig(configFile, nil)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:9090", config.BindAddr)
	assert.Equal(t, ":9091", config.HTTPAddr)
	assert.Equal(t, "debug", config.LogLevel)
	assert.Equal(t, "/tmp/gravity", config.DataDir)
}

func TestLoadConfig_Env(t *testing.T) {
	// Set environment variables
	t.Setenv("GRAVITY_ADDR", "127.0.0.1:7070")
	t.Setenv("GRAVITY_HTTP_ADDR", ":7071")
	t.Setenv("GRAVITY_LOG_LEVEL", "warn")
	t.Setenv("GRAVITY_DATA_DIR", "/var/lib/gravity")

	config, err := LoadConfig("", nil)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:7070", config.BindAddr)
	assert.Equal(t, ":7071", config.HTTPAddr)
	assert.Equal(t, "warn", config.LogLevel)
	assert.Equal(t, "/var/lib/gravity", config.DataDir)
}

func TestLoadConfig_Flags(t *testing.T) {
	// Define flags
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("addr", "127.0.0.1:8000", "bind address")
	flags.String("http_addr", ":8080", "http address")
	flags.String("log_level", "info", "log level")
	flags.String("data_dir", "./data", "data directory")

	// Parse flags
	err := flags.Parse([]string{"--addr", "127.0.0.1:6060", "--http_addr", ":6061", "--log_level", "error", "--data_dir", "/mnt/gravity"})
	require.NoError(t, err)

	config, err := LoadConfig("", flags)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:6060", config.BindAddr)
	assert.Equal(t, ":6061", config.HTTPAddr)
	assert.Equal(t, "error", config.LogLevel)
	assert.Equal(t, "/mnt/gravity", config.DataDir)
}

func TestLoadConfig_Priority(t *testing.T) {
	// 1. Setup Config File (Lowest Priority)
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gravity.yaml")
	configContent := []byte(`
addr: 127.0.0.1:1001
http_addr: :2001
log_level: trace
data_dir: /dir/config
`)
	err := os.WriteFile(configFile, configContent, 0644)
	require.NoError(t, err)

	// 2. Setup Env (Medium Priority)
	t.Setenv("GRAVITY_ADDR", "127.0.0.1:1002")
	t.Setenv("GRAVITY_LOG_LEVEL", "panic")
	// data_dir not in env, should fallback to config

	// 3. Setup Flags (Highest Priority)
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	flags.String("addr", "127.0.0.1:8000", "bind address")
	flags.String("http_addr", ":8080", "http address")
	flags.String("log_level", "info", "log level")
	flags.String("data_dir", "./data", "data directory")

	// Parse flags
	// addr is set in flag, should win over env and config
	err = flags.Parse([]string{"--addr", "127.0.0.1:1003"})
	require.NoError(t, err)

	config, err := LoadConfig(configFile, flags)
	require.NoError(t, err)

	// Addr: Flag (1003) > Env (1002) > Config (1001)
	assert.Equal(t, "127.0.0.1:1003", config.BindAddr)

	// HTTPAddr: Flag (not set) -> Env (not set) -> Config (:2001)
	assert.Equal(t, ":2001", config.HTTPAddr)

	// LogLevel: Flag (not set) -> Env (panic) > Config (trace)
	assert.Equal(t, "panic", config.LogLevel)

	// DataDir: Flag (not set) -> Env (not set) -> Config (/dir/config)
	assert.Equal(t, "/dir/config", config.DataDir)
}
