package discord

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"playstats/internal/config"
	"playstats/internal/database"
	"playstats/internal/models"
	"playstats/internal/spotify"
	"playstats/pkg/utils"
)

// Bot represents the Discord bot
type Bot struct {
	session          *discordgo.Session
	repository       *database.Repository
	spotify          *spotify.Client
	sessions         map[string]models.VoiceSession // key: guildID:userID -> voice session
	activitySessions map[string]time.Time           // key: userID:activity -> startTime
	tzUTC7           *time.Location
}

// New creates a new Discord bot
// PERBAIKAN: Parameter diubah dari 'token string' menjadi 'cfg *config.Config'
func New(cfg *config.Config, repository *database.Repository) (*Bot, error) {
	// Gunakan cfg.DiscordToken
	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildPresences |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	// Inisialisasi Spotify Client menggunakan data dari cfg
	// Kita abaikan error (nil) jika user tidak mengisi kredensial di .env
	spotifyClient, _ := spotify.New(cfg.SpotifyID, cfg.SpotifySecret)

	bot := &Bot{
		session:          session,
		repository:       repository,
		spotify:          spotifyClient, // PERBAIKAN: Masukkan client ke struct
		sessions:         make(map[string]models.VoiceSession),
		activitySessions: make(map[string]time.Time),
		tzUTC7:           time.FixedZone("UTC+7", 7*3600),
	}

	// Add event handlers
	session.AddHandler(bot.voiceStateUpdate)
	session.AddHandler(bot.messageCreate)
	session.AddHandler(bot.presenceUpdate)
	session.AddHandler(bot.interactionCreate)

	return bot, nil
}

// Start starts the bot
func (b *Bot) Start() error {
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("failed to open Discord connection: %w", err)
	}

	fmt.Println("✅ Bot is running...")
	return nil
}

// Stop stops the bot
func (b *Bot) Stop() error {
	return b.session.Close()
}

// Handler Interaksi (Klik Dropdown/Button)
func (b *Bot) interactionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type == discordgo.InteractionMessageComponent {
		// Lempar ke music.go untuk diproses
		b.handleSearchInteraction(s, i)
	}
}

// messageCreate handles message creation events
func (b *Bot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}

	content := strings.TrimSpace(m.Content)
	botUserID := s.State.User.ID
	
	// Cek mention
	isMentioned := strings.Contains(content, "<@"+botUserID+">") || strings.Contains(content, "<@!"+botUserID+">")

	switch {
	case content == "!voice" || strings.HasPrefix(content, "!voicechan"):
		b.handleVoiceCommand(s, m)
	case strings.HasPrefix(content, "!play"):
		// !play dipakai untuk statistik game, bukan musik
		b.handlePlayCommand(s, m)
	case isMentioned:
		// Handle mention (@Bot ...) -> Musik atau Stats
		b.handleMentionCommand(s, m)
	case content == "!stats":
		b.handleStatsCommand(s, m)
	case strings.HasPrefix(content, "!leaderboard"):
		b.handleLeaderboardCommand(s, m)
	case strings.HasPrefix(content, "!compare"):
		b.handleCompareCommand(s, m)
	case content == "!weekly":
		b.handleWeeklyCommand(s, m)
	case content == "!monthly":
		b.handleMonthlyCommand(s, m)
	}
}

// handleMentionCommand handles bot mention commands (Music Router)
func (b *Bot) handleMentionCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)

	// Bersihkan mention dari text
	botUserID := s.State.User.ID
	content = strings.ReplaceAll(content, "<@"+botUserID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botUserID+">", "")
	content = strings.TrimSpace(content)

	// 1. Jika kosong -> Tampilkan Stats (Default)
	if content == "" {
		b.handleStatsCommand(s, m)
		return
	}

	// 2. Cek Command Musik Spesifik
	musicCommands := []string{
		"skip", "stop", "leave",
	 	"l", "queue", "q", 
		"pause", "resume", "loop", "volume", 
		"help", "h", 
		"lyrics", "ly",
		"search", "s",
	}
	
	parts := strings.Fields(content)
	if len(parts) > 0 {
		firstWord := strings.ToLower(parts[0])
		for _, cmd := range musicCommands {
			if firstWord == cmd {
				// Arahkan ke music.go
				b.handleMusicCommand(s, m)
				return
			}
		}
	}

	// 3. Cek Link Musik (URL)
	if b.isMusicQuery(content) {
		b.handleMusicCommand(s, m)
		return
	}

	// 4. Default Fallback -> Stats
	// Jika user ngetik hal acak seperti "@Bot halo", tampilkan stats
	b.handleStatsCommand(s, m)
}

// isMusicQuery checks if the content looks like a music query
func (b *Bot) isMusicQuery(content string) bool {
	content = strings.ToLower(content)

	// Cek Link (Spotify & YouTube)
	if strings.Contains(content, "http") {
		if strings.Contains(content, "spotify") || strings.Contains(content, "youtu") {
			return true
		}
	}

	// Cek Keyword "Play" eksplisit
	if strings.HasPrefix(content, "play ") {
		return true
	}

	return false
}

// voiceStateUpdate handles voice state updates
func (b *Bot) voiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	userID := vs.UserID
	guildID := vs.GuildID
	key := guildID + ":" + userID

	// Get user info
	user, err := s.User(userID)
	username := userID
	if err == nil && user != nil {
		username = user.Username
	}

	// Join channel
	if vs.ChannelID == "" && !b.sessions[key].Start.IsZero() {
        start := b.sessions[key].Start
        channelID := b.sessions[key].ChannelID
        durationSeconds := int64(time.Since(start).Seconds())
        delete(b.sessions, key)

        // 1. Simpan Total Hours (Logic Lama - Tetap Biarkan)
        b.repository.AddVoiceSeconds(userID, guildID, durationSeconds)
        b.repository.AddChannelSeconds(userID, guildID, channelID, durationSeconds)

        // 2. FITUR BARU: Simpan Daily & Weekly Stats
        now := time.Now().In(b.tzUTC7)
        date := now.Format("2006-01-02") // Format tanggal YYYY-MM-DD
        
        // Hitung awal minggu (Senin)
        weekday := int(now.Weekday())
        if weekday == 0 { weekday = 7 } // Minggu (0) jadi 7
        weekStart := now.AddDate(0, 0, -weekday+1).Format("2006-01-02")

        // Simpan ke DB (Voice Only -> activitySeconds = 0)
        b.repository.AddDailyStats(date, userID, guildID, durationSeconds, 0, "")
        b.repository.AddWeeklyStats(weekStart, userID, guildID, durationSeconds, 0, "")

        fmt.Printf("⬅️ Leave: %s (%s), +%d seconds (Saved to Daily/Weekly)\n", username, userID, durationSeconds)
    }
}

// presenceUpdate handles presence updates for activity tracking
func (b *Bot) presenceUpdate(s *discordgo.Session, p *discordgo.PresenceUpdate) {
	guildID := p.GuildID
	userID := p.User.ID

	// Get user info
	user, err := s.User(userID)
	username := userID
	if err == nil && user != nil {
		username = user.Username
	}

	log.Printf("presenceUpdate: guild=%s user=%s (%s) activities=%d", guildID, userID, username, len(p.Activities))

	// Collect relevant activity names (Game/Application)
	activeSet := make(map[string]bool)
	for _, act := range p.Activities {
		name := act.Name
		
		// PERBAIKAN: Filter activity "Hang Status"
		if name == "Hang Status" {
			continue
		}

		if name != "" {
			activeSet[name] = true
			log.Printf("activity on: %s (%s) | %s", username, userID, name)
		}
	}

	// Close activities that were previously active but now inactive
	for key, start := range b.activitySessions {
        prefix := userID + ":"
        if !strings.HasPrefix(key, prefix) { continue }
        
        activityName := strings.TrimPrefix(key, prefix)
        if !activeSet[activityName] {
            seconds := int64(time.Since(start).Seconds())
            delete(b.activitySessions, key)
            
            // 1. Simpan Total Hours (Logic Lama)
            b.repository.AddActivitySeconds(userID, activityName, seconds)

            // 2. FITUR BARU: Simpan Daily & Weekly Stats
            now := time.Now().In(b.tzUTC7)
            date := now.Format("2006-01-02")
            
            weekday := int(now.Weekday())
            if weekday == 0 { weekday = 7 }
            weekStart := now.AddDate(0, 0, -weekday+1).Format("2006-01-02")

            // Simpan ke DB (Activity Only -> voiceSeconds = 0)
            b.repository.AddDailyStats(date, userID, guildID, 0, seconds, activityName)
            b.repository.AddWeeklyStats(weekStart, userID, guildID, 0, seconds, activityName)

            log.Printf("🎮 Activity STOP: %s | %s (+%ds) [Saved]", username, activityName, seconds)
        }
    }

	// Start new activities that haven't been recorded
	for name := range activeSet {
		key := userID + ":" + name
		if b.activitySessions[key].IsZero() {
			b.activitySessions[key] = time.Now().UTC()
			log.Printf("activity start: %s (%s) | %s", username, userID, name)
		}
	}
}

func (b *Bot) handleVoiceCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	channelHours, _ := b.repository.GetVoiceChannelHours(m.Author.ID, m.GuildID)
	var lines []string
	for _, ch := range channelHours {
		lines = append(lines, fmt.Sprintf("<#%s>: %s", ch.ChannelID, utils.FormatDuration(ch.TotalSeconds)))
	}
	totalSeconds, _ := b.repository.GetVoiceHours(m.Author.ID, m.GuildID)
	
	if len(lines) == 0 { lines = append(lines, "(belum ada data)") }
	
	msg := fmt.Sprintf("🔊 **Voice Stats** %s\nTotal: %s\n\n%s", 
		m.Author.Username, utils.FormatDuration(totalSeconds), strings.Join(lines, "\n"))
	s.ChannelMessageSend(m.ChannelID, msg)
}

func (b *Bot) handlePlayCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Command !play untuk cek durasi main game
	name := strings.TrimSpace(strings.TrimPrefix(m.Content, "!play"))
	if name == "" {
		s.ChannelMessageSend(m.ChannelID, "Format: `!play <nama game>` (Cek durasi main)")
		return
	}
	totalSeconds, _ := b.repository.GetActivityHours(m.Author.ID, name)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎮 %s, main %s selama %s", 
		m.Author.Username, name, utils.FormatDuration(totalSeconds)))
}

func (b *Bot) handleStatsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	voiceSeconds, _ := b.repository.GetVoiceHours(m.Author.ID, m.GuildID)
	activities, _ := b.repository.GetTopActivities(m.Author.ID, 5)
	
	var lines []string
	for _, act := range activities {
		lines = append(lines, fmt.Sprintf("• %s: %s", act.ActivityName, utils.FormatDuration(act.TotalSeconds)))
	}
	
	msg := fmt.Sprintf("📊 **Stats %s**\n🔊 Voice: %s\n🎮 Top Activities:\n%s", 
		m.Author.Username, utils.FormatDuration(voiceSeconds), strings.Join(lines, "\n"))
	s.ChannelMessageSend(m.ChannelID, msg)
}

func (b *Bot) handleLeaderboardCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	parts := strings.Fields(m.Content)
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "Format: `!leaderboard voice` atau `!leaderboard play <game>`")
		return
	}
	
	if parts[1] == "voice" {
		entries, _ := b.repository.GetVoiceLeaderboard(m.GuildID, 10)
		var lines []string
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("%d. <@%s> - %s", e.Rank, e.UserID, utils.FormatDuration(e.TotalSeconds)))
		}
		s.ChannelMessageSend(m.ChannelID, "🏆 **Voice Leaderboard**\n"+strings.Join(lines, "\n"))
	} else if parts[1] == "play" && len(parts) >= 3 {
		game := strings.Join(parts[2:], " ")
		entries, _ := b.repository.GetActivityLeaderboard(game, 10)
		var lines []string
		for _, e := range entries {
			lines = append(lines, fmt.Sprintf("%d. <@%s> - %s", e.Rank, e.UserID, utils.FormatDuration(e.TotalSeconds)))
		}
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🎮 **Leaderboard %s**\n%s", game, strings.Join(lines, "\n")))
	}
}

// handleActivityLeaderboard handles activity leaderboard
func (b *Bot) handleActivityLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate, activityName string) {
	entries, err := b.repository.GetActivityLeaderboard(activityName, 10)
	if err != nil {
		log.Printf("Error getting activity leaderboard: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil leaderboard aktivitas.")
		return
	}

	if len(entries) == 0 {
		s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Belum ada data untuk game '%s'.", activityName))
		return
	}

	var lines []string
	for _, entry := range entries {
		userMention := utils.FormatUserMention(entry.UserID)
		line := utils.FormatLeaderboardEntry(entry.Rank, userMention, utils.FormatDuration(entry.TotalSeconds))
		lines = append(lines, line)
	}

	msg := fmt.Sprintf("🎮 **Leaderboard %s** (Global)\n%s", activityName, strings.Join(lines, "\n"))
	s.ChannelMessageSend(m.ChannelID, msg)
}

// handleCompareCommand handles the !compare command
func (b *Bot) handleCompareCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	parts := strings.Fields(content)

	if len(parts) < 3 {
		s.ChannelMessageSend(m.ChannelID, "Format: !compare @user1 @user2")
		return
	}

	user1Mention := parts[1]
	user2Mention := parts[2]

	if !utils.IsUserMention(user1Mention) || !utils.IsUserMention(user2Mention) {
		s.ChannelMessageSend(m.ChannelID, "Format: !compare @user1 @user2")
		return
	}

	userID1 := utils.ExtractUserIDFromMention(user1Mention)
	userID2 := utils.ExtractUserIDFromMention(user2Mention)

	comparisons, err := b.repository.GetUserComparison(userID1, userID2, m.GuildID)
	if err != nil {
		log.Printf("Error getting user comparison: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil data perbandingan.")
		return
	}

	if len(comparisons) != 2 {
		s.ChannelMessageSend(m.ChannelID, "Tidak dapat menemukan data untuk salah satu atau kedua user.")
		return
	}

	user1 := comparisons[0]
	user2 := comparisons[1]

	msg := fmt.Sprintf("⚖️ **Perbandingan User**\n\n"+
		"**%s**\n"+
		"🔊 Voice: %s\n"+
		"🎮 Top Games:\n%s\n\n"+
		"**%s**\n"+
		"🔊 Voice: %s\n"+
		"🎮 Top Games:\n%s",
		user1Mention, utils.FormatDuration(user1.VoiceSeconds), b.formatTopActivities(user1.TopActivities),
		user2Mention, utils.FormatDuration(user2.VoiceSeconds), b.formatTopActivities(user2.TopActivities))

	s.ChannelMessageSend(m.ChannelID, msg)
}

// handleWeeklyCommand handles the !weekly command
func (b *Bot) handleWeeklyCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
    now := time.Now().In(b.tzUTC7)
    weekday := int(now.Weekday())
    if weekday == 0 { weekday = 7 }
    weekStart := now.AddDate(0, 0, -weekday+1).Format("2006-01-02")

    stats, err := b.repository.GetWeeklyReport(m.Author.ID, m.GuildID, weekStart)
    if err != nil || len(stats) == 0 {
        s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Belum ada data untuk minggu ini (%s).", weekStart))
        return
    }

    var voiceTotal int64
    var activityLines []string
    for _, stat := range stats {
        if stat.ActivityName == "" {
            voiceTotal += stat.VoiceSeconds
        } else {
            activityLines = append(activityLines, fmt.Sprintf("• %s: %s", stat.ActivityName, utils.FormatDuration(stat.ActivitySeconds)))
        }
    }

    msg := fmt.Sprintf("📅 **Laporan Minggu Ini** (%s)\n🔊 Voice: %s\n🎮 Activities:\n%s", 
        weekStart, utils.FormatDuration(voiceTotal), strings.Join(activityLines, "\n"))
    s.ChannelMessageSend(m.ChannelID, msg)
}

// handleMonthlyCommand handles the !monthly command
func (b *Bot) handleMonthlyCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	stats, err := b.repository.GetMonthlyReport(m.Author.ID, m.GuildID)
	if err != nil {
		log.Printf("Error getting monthly report: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil laporan bulanan.")
		return
	}

	if len(stats) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Belum ada data untuk 4 minggu terakhir.")
		return
	}

	// Group by week
	weekTotals := make(map[string]int64)
	weekActivities := make(map[string]map[string]int64)

	for _, stat := range stats {
		weekStart := stat.WeekStart
		if stat.ActivityName == "" {
			weekTotals[weekStart] += stat.VoiceSeconds
		} else {
			if weekActivities[weekStart] == nil {
				weekActivities[weekStart] = make(map[string]int64)
			}
			weekActivities[weekStart][stat.ActivityName] += stat.ActivitySeconds
		}
	}

	var lines []string
	for weekStart, voiceTotal := range weekTotals {
		line := fmt.Sprintf("**%s**: %s", weekStart, utils.FormatDuration(voiceTotal))
		if activities, exists := weekActivities[weekStart]; exists {
			var activityLines []string
			for activity, seconds := range activities {
				activityLines = append(activityLines, fmt.Sprintf("  - %s: %s",
					activity, utils.FormatDuration(seconds)))
			}
			if len(activityLines) > 0 {
				line += "\n" + strings.Join(activityLines, "\n")
			}
		}
		lines = append(lines, line)
	}

	msg := fmt.Sprintf("📊 **Laporan Bulanan** (4 minggu terakhir)\n\n%s", strings.Join(lines, "\n"))
	s.ChannelMessageSend(m.ChannelID, msg)
}

// formatTopActivities formats top activities for display
func (b *Bot) formatTopActivities(activities []database.ActivityHours) string {
	if len(activities) == 0 {
		return "  (belum ada data)"
	}

	var lines []string
	for _, activity := range activities {
		lines = append(lines, fmt.Sprintf("  - %s: %s",
			activity.ActivityName, utils.FormatDuration(activity.TotalSeconds)))
	}

	return strings.Join(lines, "\n")
}