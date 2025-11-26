package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"playstats/internal/config"
	"playstats/internal/database"
	"playstats/internal/discord"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize database
	// PERBAIKAN: Gunakan cfg.DatabaseDSN (string), bukan object cfg utuh.
	db, err := database.New(cfg.DatabaseDSN)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Create repository
	repository := database.NewRepository(db)

	// Initialize Discord bot
	bot, err := discord.New(cfg, repository)
	if err != nil {
		log.Fatalf("Failed to create Discord bot: %v", err)
	}

	// Start bot
	if err := bot.Start(); err != nil {
		log.Fatalf("Failed to start bot: %v", err)
	}

	log.Println("Bot is running. Press Ctrl+C to stop.")

	// Wait for interrupt signal
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	// --- Graceful Shutdown Logic ---
	log.Println("⚠️ Signal received, shutting down bot...")

	// Panggil Stop() secara eksplisit di sini.
	// Ini akan memicu b.SaveMusicState() yang sudah kita buat sebelumnya.
	if err := bot.Stop(); err != nil {
		log.Printf("Error stopping bot: %v", err)
	}

	// Close DB connection jika perlu (setelah bot mati)
	// if db != nil { db.Close() }

	log.Println("✅ Bot stopped gracefully.")
}
