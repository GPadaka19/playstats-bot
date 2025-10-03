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
var sessions = make(map[string]time.Time) // key: guildID:userID -> joinTime
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
	migrateSchema()

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
		user_id TEXT NOT NULL,
		guild_id TEXT NOT NULL,
		total_seconds BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, guild_id)
	)`)
	if err != nil {
		log.Fatal("DB create table error:", err)
	}
}

// Migrasi dari kolom total_minutes ke total_seconds jika masih ada skema lama
func migrateSchema() {
	// Pastikan kolom total_seconds ada (untuk versi sangat lama)
	_, _ = db.Exec(`ALTER TABLE voice_hours ADD COLUMN IF NOT EXISTS total_seconds BIGINT NOT NULL DEFAULT 0`)

	// Jika masih ada total_minutes, migrasikan ke detik lalu hapus kolom lama
	_, _ = db.Exec(`UPDATE voice_hours SET total_seconds = total_minutes * 60 WHERE total_seconds = 0 AND EXISTS (
		SELECT 1 FROM information_schema.columns WHERE table_name='voice_hours' AND column_name='total_minutes'
	)`)
	_, _ = db.Exec(`ALTER TABLE voice_hours DROP COLUMN IF EXISTS total_minutes`)

	// Tambahkan kolom guild_id bila belum ada
	_, _ = db.Exec(`ALTER TABLE voice_hours ADD COLUMN IF NOT EXISTS guild_id TEXT`)

	// Migrasi data lama yang menyimpan gabungan 'guild:user' di user_id
	_, _ = db.Exec(`UPDATE voice_hours SET guild_id = split_part(user_id, ':', 1) WHERE guild_id IS NULL AND position(':' in user_id) > 0`)
	_, _ = db.Exec(`UPDATE voice_hours SET user_id = split_part(user_id, ':', 2) WHERE position(':' in user_id) > 0`)

	// Isi nilai kosong default dan jadikan NOT NULL
	_, _ = db.Exec(`UPDATE voice_hours SET guild_id = COALESCE(guild_id, '')`)
	_, _ = db.Exec(`ALTER TABLE voice_hours ALTER COLUMN user_id SET NOT NULL`)
	_, _ = db.Exec(`ALTER TABLE voice_hours ALTER COLUMN guild_id SET NOT NULL`)

	// Pastikan primary key komposit (user_id, guild_id)
	_, _ = db.Exec(`DO $$
	DECLARE
		pk_name text;
	BEGIN
		SELECT conname INTO pk_name FROM pg_constraint
		WHERE contype = 'p' AND conrelid = 'voice_hours'::regclass;
		IF pk_name IS NOT NULL THEN
			EXECUTE format('ALTER TABLE voice_hours DROP CONSTRAINT %I', pk_name);
		END IF;
	END$$;`)
	_, _ = db.Exec(`ALTER TABLE voice_hours ADD CONSTRAINT voice_hours_pkey PRIMARY KEY (user_id, guild_id)`)
}

// Listener ketika user join/leave voice channel
func voiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	userID := vs.UserID
	guildID := vs.GuildID
	key := guildID + ":" + userID

	// Join channel
	if vs.ChannelID != "" && sessions[key].IsZero() {
		sessions[key] = time.Now().UTC()
		fmt.Println("➡️ Join:", userID, sessions[key].In(tzUTC7))
	}

	// Leave channel
	if vs.ChannelID == "" && !sessions[key].IsZero() {
		start := sessions[key]
		durationSeconds := int64(time.Since(start).Seconds())
		delete(sessions, key)

		addSeconds(userID, guildID, durationSeconds)
		fmt.Printf("⬅️ Leave: %s, +%d seconds\n", userID, durationSeconds)
	}
}


// Simpan detik ke DB
func addSeconds(userID string, guildID string, seconds int64) {
	_, err := db.Exec(`
	INSERT INTO voice_hours (user_id, guild_id, total_seconds)
	VALUES ($1, $2, $3)
	ON CONFLICT (user_id, guild_id) DO UPDATE SET total_seconds = voice_hours.total_seconds + EXCLUDED.total_seconds`,
		userID, guildID, seconds)
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
        var totalSeconds int64
        key := m.GuildID + ":" + m.Author.ID
        err := db.QueryRow("SELECT total_seconds FROM voice_hours WHERE user_id = $1", key).Scan(&totalSeconds)
		if err != nil && err != sql.ErrNoRows {
			log.Println("DB error:", err)
		}
        h := totalSeconds / 3600
        mnt := (totalSeconds % 3600) / 60
        sec := totalSeconds % 60
        msg := fmt.Sprintf("⏱️ %s, kamu sudah voice selama %d:%02d:%02d", m.Author.Username, h, mnt, sec)
		s.ChannelMessageSend(m.ChannelID, msg)
	}
}