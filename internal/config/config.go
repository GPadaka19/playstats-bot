package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration for our application
type Config struct {
	DiscordToken string
	DatabaseDSN  string
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		// .env file is optional, continue with environment variables
	}

	config := &Config{
		DiscordToken: os.Getenv("DISCORD_TOKEN"),
		DatabaseDSN:  os.Getenv("DATABASE_DSN"),
	}

	if config.DiscordToken == "" {
		return nil, &ConfigError{Field: "DISCORD_TOKEN", Message: "DISCORD_TOKEN is required"}
	}

	if config.DatabaseDSN == "" {
		return nil, &ConfigError{Field: "DATABASE_DSN", Message: "DATABASE_DSN is required"}
	}

	return config, nil
}

// ConfigError represents a configuration error
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return e.Message
}
