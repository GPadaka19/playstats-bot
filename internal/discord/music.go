package discord

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kkdai/youtube/v2"
)

// MusicTrack represents a music track
type MusicTrack struct {
	Title       string
	URL         string
	Duration    time.Duration
	Requester   string
	ChannelID   string
	Thumbnail   string
}

// MusicQueue represents a music queue for a guild
type MusicQueue struct {
	Tracks    []MusicTrack
	IsPlaying bool
	Current   int
	Loop      bool
	Volume    float64
}

// MusicSession represents a music session for a guild
type MusicSession struct {
	Queue     *MusicQueue
	VoiceConn *discordgo.VoiceConnection
	LastError error
}

// YouTube client
var ytClient = youtube.Client{}

// Music sessions per guild
var musicSessions = make(map[string]*MusicSession)

// handleMusicCommand handles music commands with bot mention
func (b *Bot) handleMusicCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	
	// Remove bot mention from content
	botUserID := s.State.User.ID
	content = strings.ReplaceAll(content, "<@"+botUserID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botUserID+">", "")
	content = strings.TrimSpace(content)
	
	if content == "" {
		s.ChannelMessageSend(m.ChannelID, "🎵 **Music Bot**\n\n"+
			"**Commands:**\n"+
			"• `@bot [song title/YouTube URL]` - Play music\n"+
			"• `@bot skip` - Skip current song\n"+
			"• `@bot stop` - Stop music\n"+
			"• `@bot queue` - Show queue\n"+
			"• `@bot pause` - Pause music\n"+
			"• `@bot resume` - Resume music\n"+
			"• `@bot loop` - Toggle loop mode\n"+
			"• `@bot volume [0-100]` - Set volume")
		return
	}
	
	// Check if user is in a voice channel
	voiceState, err := s.State.VoiceState(m.GuildID, m.Author.ID)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Kamu harus berada di voice channel terlebih dahulu!")
		return
	}
	
	// Handle different music commands
	parts := strings.Fields(content)
	command := strings.ToLower(parts[0])
	
	switch command {
	case "skip":
		b.handleSkipCommand(s, m)
	case "stop":
		b.handleStopCommand(s, m)
	case "queue":
		b.handleQueueCommand(s, m)
	case "pause":
		b.handlePauseCommand(s, m)
	case "resume":
		b.handleResumeCommand(s, m)
	case "loop":
		b.handleLoopCommand(s, m)
	case "volume":
		b.handleVolumeCommand(s, m, parts)
	default:
		// Play music - treat the entire content as search query or URL
		b.handlePlayMusic(s, m, content, voiceState.ChannelID)
	}
}

// handlePlayMusic handles playing music
func (b *Bot) handlePlayMusic(s *discordgo.Session, m *discordgo.MessageCreate, query, channelID string) {
	// Show loading message
	loadingMsg, _ := s.ChannelMessageSend(m.ChannelID, "🔍 Mencari lagu...")
	
	// Extract or search for music
	track, err := b.extractMusicInfo(query)
	if err != nil {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, "❌ Gagal mengambil informasi lagu: "+err.Error())
		return
	}
	
	// Set requester and channel
	track.Requester = m.Author.Username
	track.ChannelID = m.ChannelID
	
	// Get or create music session
	session := b.getOrCreateMusicSession(m.GuildID)
	
	// Add track to queue
	session.Queue.Tracks = append(session.Queue.Tracks, *track)
	
	// Update loading message with track info
	embed := &discordgo.MessageEmbed{
		Title: "🎵 Ditambahkan ke Queue",
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Judul",
				Value:  track.Title,
				Inline: true,
			},
			{
				Name:   "Durasi",
				Value:  track.Duration.String(),
				Inline: true,
			},
			{
				Name:   "Requested by",
				Value:  track.Requester,
				Inline: true,
			},
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{
			URL: track.Thumbnail,
		},
		Color: 0x00ff00,
	}
	
	s.ChannelMessageEditEmbed(m.ChannelID, loadingMsg.ID, embed)
	
	// Connect to voice channel if not connected
	if session.VoiceConn == nil || !session.VoiceConn.Ready {
		err := b.connectToVoice(s, m.GuildID, channelID)
		if err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal bergabung ke voice channel: "+err.Error())
			return
		}
	}
	
	// Start playing if not already playing
	if !session.Queue.IsPlaying {
		go b.startMusicPlayer(s, m.GuildID)
	}
}

// extractMusicInfo extracts music information from query/URL
func (b *Bot) extractMusicInfo(query string) (*MusicTrack, error) {
	// Check if it's a YouTube URL
	if b.isYouTubeURL(query) {
		return b.extractYouTubeInfo(query)
	}
	
	// Check if it's a Spotify URL (we'll need to convert to YouTube)
	if b.isSpotifyURL(query) {
		return b.extractSpotifyInfo(query)
	}
	
	// Treat as search query
	return b.searchYouTube(query)
}

// isYouTubeURL checks if the string is a YouTube URL
func (b *Bot) isYouTubeURL(url string) bool {
	patterns := []string{
		`^https?://(www\.)?youtube\.com/watch\?v=`,
		`^https?://youtu\.be/`,
		`^https?://(www\.)?youtube\.com/playlist\?`,
	}
	
	for _, pattern := range patterns {
		matched, _ := regexp.MatchString(pattern, url)
		if matched {
			return true
		}
	}
	return false
}

// isSpotifyURL checks if the string is a Spotify URL
func (b *Bot) isSpotifyURL(url string) bool {
	matched, _ := regexp.MatchString(`^https?://open\.spotify\.com/`, url)
	return matched
}

// extractYouTubeInfo extracts information from YouTube URL
func (b *Bot) extractYouTubeInfo(url string) (*MusicTrack, error) {
	video, err := ytClient.GetVideo(url)
	if err != nil {
		return nil, fmt.Errorf("gagal mengambil video YouTube: %v", err)
	}
	
	// Get best audio format
	formats := video.Formats.WithAudioChannels()
	if len(formats) == 0 {
		return nil, fmt.Errorf("tidak ada format audio yang tersedia")
	}
	
	// Use the first available audio format
	_ = formats[0] // format available for future use
	
	return &MusicTrack{
		Title:     video.Title,
		URL:       url,
		Duration:  video.Duration,
		Thumbnail: video.Thumbnails[0].URL,
	}, nil
}

// extractSpotifyInfo extracts information from Spotify URL (placeholder)
func (b *Bot) extractSpotifyInfo(_ string) (*MusicTrack, error) {
	// For now, we'll return an error since Spotify requires API integration
	// In a real implementation, you'd use Spotify Web API to get track info
	// and then search YouTube for the same track
	return nil, fmt.Errorf("spotify integration belum tersedia. silakan gunakan YouTube URL atau cari lagu dengan kata kunci")
}

// searchYouTube searches for a video on YouTube
func (b *Bot) searchYouTube(_ string) (*MusicTrack, error) {
	// For now, we'll create a simple search implementation
	// In a real implementation, you would use YouTube Data API v3 or similar
	// This is a placeholder that returns an error with instructions
	
	return nil, fmt.Errorf("fitur pencarian YouTube belum tersedia. silakan gunakan URL YouTube langsung atau gunakan format: `@bot https://youtube.com/watch?v=VIDEO_ID`")
}

// getOrCreateMusicSession gets or creates a music session for a guild
func (b *Bot) getOrCreateMusicSession(guildID string) *MusicSession {
	session, exists := musicSessions[guildID]
	if !exists {
		session = &MusicSession{
			Queue: &MusicQueue{
				Tracks:    make([]MusicTrack, 0),
				IsPlaying: false,
				Current:   0,
				Loop:      false,
				Volume:    0.5,
			},
		}
		musicSessions[guildID] = session
	}
	return session
}

// connectToVoice connects the bot to a voice channel
func (b *Bot) connectToVoice(s *discordgo.Session, guildID, channelID string) error {
	voiceConn, err := s.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil {
		return err
	}
	
	session := b.getOrCreateMusicSession(guildID)
	session.VoiceConn = voiceConn
	
	return nil
}

// startMusicPlayer starts the music player for a guild
func (b *Bot) startMusicPlayer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Queue.IsPlaying = true
	
	for session.Queue.Current < len(session.Queue.Tracks) {
		track := session.Queue.Tracks[session.Queue.Current]
		
		// Send now playing message
		embed := &discordgo.MessageEmbed{
			Title: "🎵 Now Playing",
			Fields: []*discordgo.MessageEmbedField{
				{
					Name:   "Judul",
					Value:  track.Title,
					Inline: true,
				},
				{
					Name:   "Durasi",
					Value:  track.Duration.String(),
					Inline: true,
				},
				{
					Name:   "Requested by",
					Value:  track.Requester,
					Inline: true,
				},
			},
			Thumbnail: &discordgo.MessageEmbedThumbnail{
				URL: track.Thumbnail,
			},
			Color: 0x00ff00,
		}
		
		s.ChannelMessageSendEmbed(track.ChannelID, embed)
		
		// Play the track (placeholder - actual audio streaming would go here)
		// For now, we'll just wait for the duration
		time.Sleep(track.Duration)
		
		// Move to next track
		session.Queue.Current++
		
		// Check if we should loop
		if session.Queue.Current >= len(session.Queue.Tracks) && session.Queue.Loop {
			session.Queue.Current = 0
		}
	}
	
	// End of queue
	session.Queue.IsPlaying = false
	session.Queue.Current = 0
}

// handleSkipCommand handles skip command
func (b *Bot) handleSkipCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	
	if len(session.Queue.Tracks) == 0 {
		s.ChannelMessageSend(m.ChannelID, "❌ Tidak ada lagu dalam queue!")
		return
	}
	
	session.Queue.Current++
	s.ChannelMessageSend(m.ChannelID, "⏭️ Melompati lagu saat ini...")
}

// handleStopCommand handles stop command
func (b *Bot) handleStopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	
	session.Queue.IsPlaying = false
	session.Queue.Tracks = make([]MusicTrack, 0)
	session.Queue.Current = 0
	
	if session.VoiceConn != nil {
		session.VoiceConn.Disconnect()
		session.VoiceConn = nil
	}
	
	s.ChannelMessageSend(m.ChannelID, "⏹️ Musik dihentikan dan queue dibersihkan.")
}

// handleQueueCommand handles queue command
func (b *Bot) handleQueueCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	
	if len(session.Queue.Tracks) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📋 Queue kosong!")
		return
	}
	
	var queueText strings.Builder
	queueText.WriteString("📋 **Music Queue**\n\n")
	
	for i, track := range session.Queue.Tracks {
		status := ""
		if i == session.Queue.Current {
			status = "🎵 **Now Playing**"
		} else if i < session.Queue.Current {
			status = "✅"
		} else {
			status = fmt.Sprintf("%d.", i+1)
		}
		
		queueText.WriteString(fmt.Sprintf("%s %s - %s\n", status, track.Title, track.Duration.String()))
	}
	
	s.ChannelMessageSend(m.ChannelID, queueText.String())
}

// handlePauseCommand handles pause command
func (b *Bot) handlePauseCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	
	if !session.Queue.IsPlaying {
		s.ChannelMessageSend(m.ChannelID, "❌ Tidak ada musik yang sedang diputar!")
		return
	}
	
	// Note: Actual pause implementation would require audio stream control
	s.ChannelMessageSend(m.ChannelID, "⏸️ Musik dijeda.")
}

// handleResumeCommand handles resume command
func (b *Bot) handleResumeCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	
	if session.Queue.IsPlaying {
		s.ChannelMessageSend(m.ChannelID, "❌ Musik sudah diputar!")
		return
	}
	
	// Note: Actual resume implementation would require audio stream control
	s.ChannelMessageSend(m.ChannelID, "▶️ Musik dilanjutkan.")
}

// handleLoopCommand handles loop command
func (b *Bot) handleLoopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	
	session.Queue.Loop = !session.Queue.Loop
	
	status := "❌ OFF"
	if session.Queue.Loop {
		status = "✅ ON"
	}
	
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔁 Loop mode: %s", status))
}

// handleVolumeCommand handles volume command
func (b *Bot) handleVolumeCommand(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Format: `@bot volume [0-100]`")
		return
	}
	
	// Parse volume (this would need proper validation in real implementation)
	_ = b.getOrCreateMusicSession(m.GuildID)
	
	// Note: Actual volume implementation would require audio stream control
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔊 Volume diatur ke: %s", parts[1]))
}
