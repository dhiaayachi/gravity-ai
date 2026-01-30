package cmd

import (
	"os"

	"github.com/dhiaayachi/gravity-ai/config"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var (
	cfgFile string
	cfg     *config.Config
	// appLogger is the global logger
	appLogger *zap.Logger
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "gravity",
	Short: "Gravity AI Agent",
	Long:  `Gravity AI Agent and CLI tool.`,
	// Uncomment the following line if your bare application
	// has an action associated with it:
	// Run: func(cmd *cobra.Command, args []string) { },
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Global Flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./gravity.yaml)")
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")

	cobra.OnInitialize(initConfig)
}

func initConfig() {
	// Initialize Logger temporarily to log config errors if needed, or just standard log
	// Real logger init happens after we have config, but we have flags now.

	// We need to pass flags to config loader.
	// Since we are in Cobra, we can pass rootCmd.Flags() or just rely on binding?
	// The config.LoadConfig expects *pflag.FlagSet.

	// We might need to access flags from the specific command being run,
	// but for now let's pass the persistent flags + command flags.
	// Merging flags is tricky. Let's see how LoadConfig works.
	// It takes `flags *pflag.FlagSet`.

	// For now we will allow subcommands to init their own config if they need specific flags,
	// or we init here.
	// Ideally, we load config once.
}

// GetConfig returns the loaded configuration
func GetConfig() *config.Config {
	return cfg
}

// GetLogger returns the initialized logger
func GetLogger() *zap.Logger {
	return appLogger
}

// SetLogger sets the logger
func SetLogger(l *zap.Logger) {
	appLogger = l
}

// SetConfig sets the config
func SetConfig(c *config.Config) {
	cfg = c
}
