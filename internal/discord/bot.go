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

	switch {
	case content == "!voice" || strings.HasPrefix(content, "!voicechan"):
		b.handleVoiceCommand(s, m)
	case strings.HasPrefix(content, "!play"):
		b.handlePlayCommand(s, m)
	case content == "!stats":
		b.handleStatsCommand(s, m)
	}
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
