package discord

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"playstats/internal/database"
	"playstats/internal/models"
	"playstats/pkg/utils"
)

// Bot represents the Discord bot
type Bot struct {
	session     *discordgo.Session
	repository  *database.Repository
	sessions    map[string]models.VoiceSession // key: guildID:userID -> voice session
	activitySessions map[string]time.Time     // key: userID:activity -> startTime
	tzUTC7      *time.Location
}

// New creates a new Discord bot
func New(token string, repository *database.Repository) (*Bot, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("failed to create Discord session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuilds | 
		discordgo.IntentsGuildPresences | 
		discordgo.IntentsGuildVoiceStates | 
		discordgo.IntentsGuildMessages

	bot := &Bot{
		session:          session,
		repository:       repository,
		sessions:         make(map[string]models.VoiceSession),
		activitySessions: make(map[string]time.Time),
		tzUTC7:           time.FixedZone("UTC+7", 7*3600),
	}

	// Add event handlers
	session.AddHandler(bot.voiceStateUpdate)
	session.AddHandler(bot.messageCreate)
	session.AddHandler(bot.presenceUpdate)

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

// voiceStateUpdate handles voice state updates
func (b *Bot) voiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	userID := vs.UserID
	guildID := vs.GuildID
	key := guildID + ":" + userID

	// Join channel
	if vs.ChannelID != "" && b.sessions[key].Start.IsZero() {
		b.sessions[key] = models.VoiceSession{
			Start:     time.Now().UTC(),
			ChannelID: vs.ChannelID,
		}
		fmt.Printf("➡️ Join: %s %s channel=%s\n", userID, b.sessions[key].Start.In(b.tzUTC7), vs.ChannelID)
	}

	// Leave channel
	if vs.ChannelID == "" && !b.sessions[key].Start.IsZero() {
		start := b.sessions[key].Start
		channelID := b.sessions[key].ChannelID
		durationSeconds := int64(time.Since(start).Seconds())
		delete(b.sessions, key)

		if err := b.repository.AddVoiceSeconds(userID, guildID, durationSeconds); err != nil {
			log.Printf("Error adding voice seconds: %v", err)
		}
		if err := b.repository.AddChannelSeconds(userID, guildID, channelID, durationSeconds); err != nil {
			log.Printf("Error adding channel seconds: %v", err)
		}
		fmt.Printf("⬅️ Leave: %s, +%d seconds channel=%s\n", userID, durationSeconds, channelID)
	}
}

// presenceUpdate handles presence updates for activity tracking
func (b *Bot) presenceUpdate(s *discordgo.Session, p *discordgo.PresenceUpdate) {
	guildID := p.GuildID
	userID := p.User.ID
	log.Printf("presenceUpdate: guild=%s user=%s activities=%d", guildID, userID, len(p.Activities))

	// Collect relevant activity names (Game/Application)
	activeSet := make(map[string]bool)
	for _, act := range p.Activities {
		name := act.Name
		if name != "" {
			activeSet[name] = true
			log.Printf("activity on: %s | %s", userID, name)
		}
	}

	// Close activities that were previously active but now inactive
	for key, start := range b.activitySessions {
		// key format: user:activity (global)
		prefix := userID + ":"
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		activityName := strings.TrimPrefix(key, prefix)
		if !activeSet[activityName] {
			// accumulate duration
			seconds := int64(time.Since(start).Seconds())
			delete(b.activitySessions, key)
			if err := b.repository.AddActivitySeconds(userID, activityName, seconds); err != nil {
				log.Printf("Error adding activity seconds: %v", err)
			}
			log.Printf("activity off: %s | %s +%ds", userID, activityName, seconds)
		}
	}

	// Start new activities that haven't been recorded
	for name := range activeSet {
		key := userID + ":" + name
		if b.activitySessions[key].IsZero() {
			b.activitySessions[key] = time.Now().UTC()
			log.Printf("activity start: %s | %s", userID, name)
		}
	}
}

// messageCreate handles message creation events
func (b *Bot) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}

	content := strings.TrimSpace(m.Content)
	botUserID := s.State.User.ID // ambil ID bot
	isMentioned := strings.Contains(content, "<@"+botUserID+">") || strings.Contains(content, "<@!"+botUserID+">")

	switch {
	case content == "!voice" || strings.HasPrefix(content, "!voicechan"):
		b.handleVoiceCommand(s, m)
	case strings.HasPrefix(content, "!play"):
		b.handlePlayCommand(s, m)
	case isMentioned:
		// Handle bot mention commands (music or stats)
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

// handleMentionCommand handles bot mention commands
func (b *Bot) handleMentionCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	
	// Remove bot mention from content to get the actual command
	botUserID := s.State.User.ID
	content = strings.ReplaceAll(content, "<@"+botUserID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botUserID+">", "")
	content = strings.TrimSpace(content)
	
	// Check if it's a music-related command or just stats
	if content == "" || strings.ToLower(content) == "stats" {
		// Default to stats if no specific command or "stats"
		b.handleStatsCommand(s, m)
		return
	}
	
	// Check if it's a music command
	musicCommands := []string{"skip", "stop", "queue", "pause", "resume", "loop", "volume"}
	parts := strings.Fields(content)
	if len(parts) > 0 {
		firstWord := strings.ToLower(parts[0])
		for _, cmd := range musicCommands {
			if firstWord == cmd {
				b.handleMusicCommand(s, m)
				return
			}
		}
	}
	
	// If it contains URL patterns or seems like a search query, treat as music
	if b.isMusicQuery(content) {
		b.handleMusicCommand(s, m)
		return
	}
	
	// Default to stats for anything else
	b.handleStatsCommand(s, m)
}

// isMusicQuery checks if the content looks like a music query
func (b *Bot) isMusicQuery(content string) bool {
	// Check for YouTube URLs
	youtubePatterns := []string{
		"youtube.com",
		"youtu.be",
	}
	
	// Check for Spotify URLs
	spotifyPatterns := []string{
		"spotify.com",
	}
	
	content = strings.ToLower(content)
	
	// Check for URL patterns
	for _, pattern := range append(youtubePatterns, spotifyPatterns...) {
		if strings.Contains(content, pattern) {
			return true
		}
	}
	
	// If it's more than 3 words and doesn't look like a command, treat as search query
	words := strings.Fields(content)
	return len(words) > 3
}

// handleVoiceCommand handles the !voice command
func (b *Bot) handleVoiceCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	channelHours, err := b.repository.GetVoiceChannelHours(m.Author.ID, m.GuildID)
	if err != nil {
		log.Printf("Error getting voice channel hours: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil data voice per channel.")
		return
	}

	var lines []string
	for _, ch := range channelHours {
		lines = append(lines, fmt.Sprintf("<#%s>: %s", ch.ChannelID, utils.FormatDuration(ch.TotalSeconds)))
	}

	// Get total overall
	totalSeconds, err := b.repository.GetVoiceHours(m.Author.ID, m.GuildID)
	if err != nil {
		log.Printf("Error getting total voice hours: %v", err)
	}

	if len(lines) == 0 {
		lines = append(lines, "(belum ada data per channel)")
	}

	msg := fmt.Sprintf("🔊 %s, voice per channel:\n%s\nTotal: %s", 
		m.Author.Username, strings.Join(lines, "\n"), utils.FormatDuration(totalSeconds))
	s.ChannelMessageSend(m.ChannelID, msg)
}

// handlePlayCommand handles the !play command
func (b *Bot) handlePlayCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	name := strings.TrimSpace(strings.TrimPrefix(content, "!play"))
	if name == "" {
		s.ChannelMessageSend(m.ChannelID, "Format: !play <nama game/aplikasi>")
		return
	}

	totalSeconds, err := b.repository.GetActivityHours(m.Author.ID, name)
	if err != nil {
		log.Printf("Error getting activity hours: %v", err)
	}

	msg := fmt.Sprintf("🎮 %s, %s selama %s", m.Author.Username, name, utils.FormatDuration(totalSeconds))
	s.ChannelMessageSend(m.ChannelID, msg)
}

// handleStatsCommand handles the !stats command
func (b *Bot) handleStatsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Get total voice hours for this guild
	voiceSeconds, err := b.repository.GetVoiceHours(m.Author.ID, m.GuildID)
	if err != nil {
		log.Printf("Error getting voice hours: %v", err)
	}

	// Get top activities
	activities, err := b.repository.GetTopActivities(m.Author.ID, 5)
	if err != nil {
		log.Printf("Error getting top activities: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil statistik.")
		return
	}

	var lines []string
	for _, activity := range activities {
		lines = append(lines, fmt.Sprintf("- %s: %s", activity.ActivityName, utils.FormatDuration(activity.TotalSeconds)))
	}

	msg := fmt.Sprintf("📊 %s\nVoice (server ini): %s\nAktivitas teratas (global):\n%s", 
		m.Author.Username, utils.FormatDuration(voiceSeconds), strings.Join(lines, "\n"))
	s.ChannelMessageSend(m.ChannelID, msg)
}

// handleLeaderboardCommand handles the !leaderboard command
func (b *Bot) handleLeaderboardCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	parts := strings.Fields(content)
	
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "Format: !leaderboard voice | !leaderboard play <nama game>")
		return
	}
	
	switch parts[1] {
	case "voice":
		b.handleVoiceLeaderboard(s, m)
	case "play":
		if len(parts) < 3 {
			s.ChannelMessageSend(m.ChannelID, "Format: !leaderboard play <nama game>")
			return
		}
		gameName := strings.Join(parts[2:], " ")
		b.handleActivityLeaderboard(s, m, gameName)
	default:
		s.ChannelMessageSend(m.ChannelID, "Format: !leaderboard voice | !leaderboard play <nama game>")
	}
}

// handleVoiceLeaderboard handles voice leaderboard
func (b *Bot) handleVoiceLeaderboard(s *discordgo.Session, m *discordgo.MessageCreate) {
	entries, err := b.repository.GetVoiceLeaderboard(m.GuildID, 10)
	if err != nil {
		log.Printf("Error getting voice leaderboard: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil leaderboard voice.")
		return
	}
	
	if len(entries) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Belum ada data voice untuk leaderboard.")
		return
	}
	
	var lines []string
	for _, entry := range entries {
		userMention := utils.FormatUserMention(entry.UserID)
		line := utils.FormatLeaderboardEntry(entry.Rank, userMention, utils.FormatDuration(entry.TotalSeconds))
		lines = append(lines, line)
	}
	
	msg := fmt.Sprintf("🏆 **Voice Leaderboard** (Server ini)\n%s", strings.Join(lines, "\n"))
	s.ChannelMessageSend(m.ChannelID, msg)
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
	// Get current week start (Monday)
	now := time.Now()
	weekStart := now.AddDate(0, 0, -int(now.Weekday())+1).Format("2006-01-02")
	
	stats, err := b.repository.GetWeeklyReport(m.Author.ID, m.GuildID, weekStart)
	if err != nil {
		log.Printf("Error getting weekly report: %v", err)
		s.ChannelMessageSend(m.ChannelID, "Terjadi kesalahan mengambil laporan mingguan.")
		return
	}
	
	if len(stats) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Belum ada data untuk minggu ini.")
		return
	}
	
	var voiceTotal int64
	var activityLines []string
	
	for _, stat := range stats {
		if stat.ActivityName == "" {
			voiceTotal += stat.VoiceSeconds
		} else {
			activityLines = append(activityLines, fmt.Sprintf("- %s: %s", 
				stat.ActivityName, utils.FormatDuration(stat.ActivitySeconds)))
		}
	}
	
	msg := fmt.Sprintf("📅 **Laporan Mingguan** (%s)\n\n"+
		"🔊 Total Voice: %s\n"+
		"🎮 Aktivitas:\n%s",
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
