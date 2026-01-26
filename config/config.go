package config

import (
	"errors"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config holds the application configuration.
type Config struct {
	// General
	LogLevel string `mapstructure:"log-level"`

	// Network
	BindAddr string `mapstructure:"bind-addr"`
	HTTPAddr string `mapstructure:"http-addr"`

	// Raft
	ID        string `mapstructure:"agent-id"`
	DataDir   string `mapstructure:"data-dir"`
	Bootstrap bool   `mapstructure:"bootstrap"`
	Peers     string `mapstructure:"peers"`

	// LLM
	LLMProvider string `mapstructure:"llm-provider"`
	APIKey      string `mapstructure:"api-key"` // Watch out for secrets!
	Model       string `mapstructure:"model"`
	OllamaURL   string `mapstructure:"ollama-url"`
}

// LoadConfig loads configuration from CLI flags, environment variables, and config file.
// Priority: CLI Flags > Environment Variables > Config File > Defaults.
func LoadConfig(cfgFile string, flags *pflag.FlagSet) (*Config, error) {
	v := viper.New()

	// 1. Set Defaults
	v.SetDefault("log-level", "info")
	v.SetDefault("bind-addr", "127.0.0.1:8000")
	v.SetDefault("http-addr", ":8080")
	v.SetDefault("agent-id", "agent-1")
	v.SetDefault("data-dir", "./data")
	v.SetDefault("bootstrap", false)
	v.SetDefault("llm-provider", "mock")
	v.SetDefault("ollama-url", "http://localhost:11434")

	// 2. Load Config File
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("gravity")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/gravity")
		v.AddConfigPath("$HOME/.gravity")
	}

	if err := v.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if !errors.As(err, &configFileNotFoundError) {
			return nil, err
		}
		// Config file is optional
	}

	// 3. Load Environment Variables (GRAVITY_ prefix)
	v.SetEnvPrefix("GRAVITY")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()

	// 4. Bind Flags
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return nil, err
		}
	}

	// Unmarshal into struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, err
	}
	v.Debug()
	return &config, nil
}
