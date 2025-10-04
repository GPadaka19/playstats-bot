package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"playstats/internal/config"
	"playstats/internal/database"
	"playstats/internal/discord"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize database
	db, err := database.New(cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Create repository
	repository := database.NewRepository(db)

	// Initialize Discord bot
	bot, err := discord.New(cfg.DiscordToken, repository)
	if err != nil {
		log.Fatalf("Failed to create Discord bot: %v", err)
	}

	// Start bot
	if err := bot.Start(); err != nil {
		log.Fatalf("Failed to start bot: %v", err)
	}
	defer bot.Stop()

	// Wait for interrupt signal
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("Shutting down bot...")
}
