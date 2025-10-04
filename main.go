package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/bwmarrin/discordgo"
	_ "github.com/lib/pq"
)

var db *sql.DB

type VoiceSession struct {
	start     time.Time
	channelID string
}

var sessions = make(map[string]VoiceSession) // key: guildID:userID -> voice session (UTC start + channel)
var activitySessions = make(map[string]time.Time) // key: guildID:userID:activity -> startTime
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

	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildPresences | discordgo.IntentsGuildVoiceStates | discordgo.IntentsGuildMessages

	dg.AddHandler(voiceStateUpdate)
	dg.AddHandler(messageCreate)
	dg.AddHandler(presenceUpdate)

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

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS activity_hours (
		user_id TEXT NOT NULL,
		activity_name TEXT NOT NULL,
		total_seconds BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, activity_name)
	)`)
	if err != nil {
		log.Fatal("DB create activity table error:", err)
	}

	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS voice_channel_hours (
		user_id TEXT NOT NULL,
		guild_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		total_seconds BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, guild_id, channel_id)
	)`)
	if err != nil {
		log.Fatal("DB create voice channel table error:", err)
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

	// Tambahkan kolom guild_id bila belum ada di voice_hours
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

	// Migrasi activity_hours lama (jika ada guild_id) menjadi global teragregasi
	_, _ = db.Exec(`
	CREATE TABLE IF NOT EXISTS activity_hours_new (
		user_id TEXT NOT NULL,
		activity_name TEXT NOT NULL,
		total_seconds BIGINT NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, activity_name)
	)`)
	// Agregasi dari skema lama ke baru
	_, _ = db.Exec(`
	INSERT INTO activity_hours_new (user_id, activity_name, total_seconds)
	SELECT user_id, activity_name, SUM(total_seconds)
	FROM activity_hours
	GROUP BY user_id, activity_name
	ON CONFLICT (user_id, activity_name) DO UPDATE SET total_seconds = activity_hours_new.total_seconds + EXCLUDED.total_seconds`)
	// Ganti tabel
	_, _ = db.Exec(`DROP TABLE IF EXISTS activity_hours`)
	_, _ = db.Exec(`ALTER TABLE activity_hours_new RENAME TO activity_hours`)
}

// Listener ketika user join/leave voice channel
func voiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	userID := vs.UserID
	guildID := vs.GuildID
	key := guildID + ":" + userID

	// Join channel
	if vs.ChannelID != "" && sessions[key].start.IsZero() {
		sessions[key] = VoiceSession{start: time.Now().UTC(), channelID: vs.ChannelID}
		fmt.Println("➡️ Join:", userID, sessions[key].start.In(tzUTC7), "channel=", vs.ChannelID)
	}

	// Leave channel
	if vs.ChannelID == "" && !sessions[key].start.IsZero() {
		start := sessions[key].start
		channelID := sessions[key].channelID
		durationSeconds := int64(time.Since(start).Seconds())
		delete(sessions, key)

		addSeconds(userID, guildID, durationSeconds)
		addChannelSeconds(userID, guildID, channelID, durationSeconds)
		fmt.Printf("⬅️ Leave: %s, +%d seconds channel=%s\n", userID, durationSeconds, channelID)
	}
}

// Handler presence untuk melacak aktivitas bermain
func presenceUpdate(s *discordgo.Session, p *discordgo.PresenceUpdate) {
	guildID := p.GuildID
	userID := p.User.ID
	log.Printf("presenceUpdate: guild=%s user=%s activities=%d", guildID, userID, len(p.Activities))

	// Kumpulkan nama aktivitas yang relevan (Game/Application)
	activeSet := make(map[string]bool)
	for _, act := range p.Activities {
		name := act.Name
		if name != "" {
			activeSet[name] = true
			log.Printf("activity on: %s | %s", userID, name)
		}
	}

	// Tutup aktivitas yang sebelumnya aktif tapi kini tidak
	for key, start := range activitySessions {
		// key format: user:activity (global)
		prefix := userID + ":"
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		activityName := strings.TrimPrefix(key, prefix)
		if !activeSet[activityName] {
			// akumulasi durasi
			seconds := int64(time.Since(start).Seconds())
			delete(activitySessions, key)
			addActivitySeconds(userID, activityName, seconds)
			log.Printf("activity off: %s | %s +%ds", userID, activityName, seconds)
		}
	}

	// Mulai aktivitas baru yang belum tercatat
	for name := range activeSet {
		key := userID + ":" + name
		if activitySessions[key].IsZero() {
			activitySessions[key] = time.Now().UTC()
			log.Printf("activity start: %s | %s", userID, name)
		}
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

// Simpan detik aktivitas ke DB (global)
func addActivitySeconds(userID, activityName string, seconds int64) {
	_, err := db.Exec(`
	INSERT INTO activity_hours (user_id, activity_name, total_seconds)
	VALUES ($1, $2, $3)
	ON CONFLICT (user_id, activity_name) DO UPDATE SET total_seconds = activity_hours.total_seconds + EXCLUDED.total_seconds`,
		userID, activityName, seconds)
	if err != nil {
		log.Println("DB activity error:", err)
	}
}

// Simpan detik per channel ke DB
func addChannelSeconds(userID, guildID, channelID string, seconds int64) {
	_, err := db.Exec(`
	INSERT INTO voice_channel_hours (user_id, guild_id, channel_id, total_seconds)
	VALUES ($1, $2, $3, $4)
	ON CONFLICT (user_id, guild_id, channel_id) DO UPDATE SET total_seconds = voice_channel_hours.total_seconds + EXCLUDED.total_seconds`,
		userID, guildID, channelID, seconds)
	if err != nil {
		log.Println("DB voice channel error:", err)
	}
}

func formatDuration(totalSeconds int64) string {
	h := totalSeconds / 3600
	m := (totalSeconds % 3600) / 60
	s := totalSeconds % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// Command !stats
func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}
	content := strings.TrimSpace(m.Content)
	if content == "!voice" || strings.HasPrefix(content, "!voicechan") {
		// Ambil total per channel (urut desc)
		rows, err := db.Query("SELECT channel_id, total_seconds FROM voice_channel_hours WHERE user_id = $1 AND guild_id = $2 ORDER BY total_seconds DESC", m.Author.ID, m.GuildID)
		if err != nil {
			log.Println("DB error:", err)
			s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil data voice per channel.")
			return
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var channelID string
			var sec int64
			if err := rows.Scan(&channelID, &sec); err == nil {
				lines = append(lines, fmt.Sprintf("<#%s>: %s", channelID, formatDuration(sec)))
			}
		}
		// Ambil total keseluruhan
		var totalSeconds int64
		_ = db.QueryRow("SELECT total_seconds FROM voice_hours WHERE user_id = $1 AND guild_id = $2", m.Author.ID, m.GuildID).Scan(&totalSeconds)
		if len(lines) == 0 {
			lines = append(lines, "(belum ada data per channel)")
		}
		msg := fmt.Sprintf("🔊 %s, voice per channel:\n%s\nTotal: %s", m.Author.Username, strings.Join(lines, "\n"), formatDuration(totalSeconds))
		s.ChannelMessageSend(m.ChannelID, msg)
		return
	}
	if strings.HasPrefix(content, "!play") {
		name := strings.TrimSpace(strings.TrimPrefix(content, "!play"))
		if name == "" {
			s.ChannelMessageSend(m.ChannelID, "Format: !play <nama game/aplikasi>")
			return
		}
		var totalSeconds int64
		err := db.QueryRow("SELECT total_seconds FROM activity_hours WHERE user_id = $1 AND activity_name = $2", m.Author.ID, name).Scan(&totalSeconds)
		if err != nil && err != sql.ErrNoRows {
			log.Println("DB error:", err)
		}
		msg := fmt.Sprintf("🎮 %s, %s selama %s", m.Author.Username, name, formatDuration(totalSeconds))
		s.ChannelMessageSend(m.ChannelID, msg)
		return
	}
	if content == "!stats" {
		// ringkas: total voice (per guild) + 3 aktivitas global teratas
		var voiceSeconds int64
		_ = db.QueryRow("SELECT total_seconds FROM voice_hours WHERE user_id = $1 AND guild_id = $2", m.Author.ID, m.GuildID).Scan(&voiceSeconds)

		rows, err := db.Query("SELECT activity_name, total_seconds FROM activity_hours WHERE user_id = $1 ORDER BY total_seconds DESC LIMIT 5", m.Author.ID)
		if err != nil {
			log.Println("DB error:", err)
			s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil statistik.")
			return
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var name string
			var sec int64
			if err := rows.Scan(&name, &sec); err == nil {
				lines = append(lines, fmt.Sprintf("- %s: %s", name, formatDuration(sec)))
			}
		}
		msg := fmt.Sprintf("📊 %s\nVoice (server ini): %s\nAktivitas teratas (global):\n%s", m.Author.Username, formatDuration(voiceSeconds), strings.Join(lines, "\n"))
		s.ChannelMessageSend(m.ChannelID, msg)
		return
	}
}