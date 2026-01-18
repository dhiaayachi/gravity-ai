package config

import (
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config holds the application configuration.
type Config struct {
	// General
	LogLevel string `mapstructure:"log_level"`

	// Network
	BindAddr string `mapstructure:"addr"`
	HTTPAddr string `mapstructure:"http_addr"`

	// Raft
	ID        string            `mapstructure:"id"`
	DataDir   string            `mapstructure:"data_dir"`
	Bootstrap bool              `mapstructure:"bootstrap"`
	Peers     map[string]string `mapstructure:"peers"`

	// LLM
	LLMProvider string `mapstructure:"llm_provider"`
	APIKey      string `mapstructure:"api_key"` // Watch out for secrets!
	Model       string `mapstructure:"model"`
	OllamaURL   string `mapstructure:"ollama_url"`
}

// LoadConfig loads configuration from CLI flags, environment variables, and config file.
// Priority: CLI Flags > Environment Variables > Config File > Defaults.
func LoadConfig(cfgFile string, flags *pflag.FlagSet) (*Config, error) {
	v := viper.New()

	// 1. Set Defaults
	v.SetDefault("log_level", "info")
	v.SetDefault("addr", "127.0.0.1:8000")
	v.SetDefault("http_addr", ":8080")
	v.SetDefault("id", "agent-1")
	v.SetDefault("data_dir", "./data")
	v.SetDefault("bootstrap", false)
	v.SetDefault("llm_provider", "mock")
	v.SetDefault("ollama_url", "http://localhost:11434")

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
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
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

	return &config, nil
}
