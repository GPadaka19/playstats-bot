package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	"github.com/bwmarrin/discordgo"
	_ "github.com/lib/pq"
)

var db *sql.DB
var sessions = make(map[string]time.Time) // userID -> joinTime
var tzUTC7 = time.FixedZone("UTC+7", 7*3600)

func main() {
    if err := godotenv.Load(); err != nil {
        log.Println(".env not found, using environment variables")
    }

	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN not set")
	}

	// PostgreSQL connection string
	pgURL := os.Getenv("DATABASE_DSN")
	if pgURL == "" {
		log.Fatal("DATABASE_DSN not set")
	}

    var err error
	db, err = sql.Open("postgres", pgURL)
	if err != nil {
		log.Fatal("DB connect error:", err)
	}
	err = db.Ping()
	if err != nil {
		log.Fatal("DB ping error:", err)
	}
	createTable()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("error creating Discord session,", err)
	}

	dg.Identify.Intents = discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMessages

	dg.AddHandler(voiceStateUpdate)
	dg.AddHandler(messageCreate)

	err = dg.Open()
	if err != nil {
		log.Fatal("error opening connection,", err)
	}
	defer dg.Close()

	fmt.Println("✅ Bot is running...")
	select {}
}

func createTable() {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS voice_hours (
		user_id TEXT PRIMARY KEY,
		total_minutes BIGINT NOT NULL DEFAULT 0
	)`)
	if err != nil {
		log.Fatal("DB create table error:", err)
	}
}

// Listener ketika user join/leave voice channel
func voiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	userID := vs.UserID

    // Join channel
    if vs.ChannelID != "" && sessions[userID].IsZero() {
        sessions[userID] = time.Now().UTC()
        fmt.Println("➡️ Join:", userID, sessions[userID].In(tzUTC7))
    }

    // Leave channel
    if vs.ChannelID == "" && !sessions[userID].IsZero() {
        start := sessions[userID]
        duration := time.Since(start).Minutes()
        delete(sessions, userID)

        addMinutes(userID, int64(duration))
        fmt.Printf("⬅️ Leave: %s, +%.1f minutes\n", userID, duration)
    }
}

// Simpan menit ke DB
func addMinutes(userID string, minutes int64) {
	_, err := db.Exec(`
	INSERT INTO voice_hours (user_id, total_minutes)
	VALUES ($1, $2)
	ON CONFLICT (user_id) DO UPDATE SET total_minutes = voice_hours.total_minutes + EXCLUDED.total_minutes`,
		userID, minutes)
	if err != nil {
		log.Println("DB error:", err)
	}
}

// Command !stats
func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}
	if m.Content == "!stats" {
		var total int64
		err := db.QueryRow("SELECT total_minutes FROM voice_hours WHERE user_id = $1", m.Author.ID).Scan(&total)
		if err != nil && err != sql.ErrNoRows {
			log.Println("DB error:", err)
		}
        totalSeconds := total * 60
        h := totalSeconds / 3600
        mnt := (totalSeconds % 3600) / 60
        sec := totalSeconds % 60
        msg := fmt.Sprintf("⏱️ %s, kamu sudah voice selama %d:%02d:%02d", m.Author.Username, h, mnt, sec)
		s.ChannelMessageSend(m.ChannelID, msg)
	}
}