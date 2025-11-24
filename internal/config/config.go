package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds all configuration for our application
type Config struct {
	DiscordToken 	string
	DatabaseDSN  	string
	SpotifyID		string
	SpotifySecret	string
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
	}

	config := &Config{
		DiscordToken: 	os.Getenv("DISCORD_TOKEN"),
		DatabaseDSN:  	os.Getenv("DATABASE_DSN"),
		SpotifyID: 	  	os.Getenv("SPOTIFY_ID"),
		SpotifySecret:	os.Getenv("SPOTIFY_SECRET"),
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
