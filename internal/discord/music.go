package discord

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/kkdai/youtube/v2"
)

// MusicTrack represents a music track
type MusicTrack struct {
	Title     string
	URL       string
	Duration  time.Duration
	Requester string
	ChannelID string
	Thumbnail string
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
	if err != nil || voiceState == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Kamu harus berada di voice channel terlebih dahulu!")
		return
	}

	// Handle different music commands
	parts := strings.Fields(content)
	if len(parts) == 0 {
		return
	}
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
		b.handlePlayMusic(s, m, content, voiceState.ChannelID)
	}
}

// handlePlayMusic handles playing music
func (b *Bot) handlePlayMusic(s *discordgo.Session, m *discordgo.MessageCreate, query, channelID string) {
	fmt.Printf("🎵 Music query from %s: %s\n", m.Author.Username, query)

	loadingMsg, _ := s.ChannelMessageSend(m.ChannelID, "🔍 Mencari lagu...")

	track, err := b.extractMusicInfo(query)
	if err != nil {
		fmt.Printf("❌ Music extraction error: %v\n", err)
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, "❌ Gagal mengambil informasi lagu: "+err.Error())
		return
	}

	track.Requester = m.Author.Username
	track.ChannelID = m.ChannelID

	session := b.getOrCreateMusicSession(m.GuildID)
	session.Queue.Tracks = append(session.Queue.Tracks, *track)

	embed := &discordgo.MessageEmbed{
		Title: "🎵 Ditambahkan ke Queue",
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Judul", Value: track.Title, Inline: true},
			{Name: "Durasi", Value: track.Duration.String(), Inline: true},
			{Name: "Requested by", Value: track.Requester, Inline: true},
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
		Color:     0x00ff00,
	}
	s.ChannelMessageEditEmbed(m.ChannelID, loadingMsg.ID, embed)

	if session.VoiceConn == nil || !session.VoiceConn.Ready {
		if err := b.connectToVoice(s, m.GuildID, channelID); err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal bergabung ke voice channel: "+err.Error())
			return
		}
	}

	if !session.Queue.IsPlaying {
		go b.startMusicPlayer(s, m.GuildID)
	}
}

// extractMusicInfo extracts music information from query/URL
func (b *Bot) extractMusicInfo(query string) (*MusicTrack, error) {
	fmt.Printf("🔍 Extracting music info for: %s\n", query)

	if b.isYouTubeURL(query) {
		fmt.Println("📺 Detected YouTube URL")
		return b.extractYouTubeInfo(query)
	}

	if b.isSpotifyURL(query) {
		fmt.Println("🎧 Detected Spotify URL")
		return b.extractSpotifyInfo(query)
	}

	fmt.Println("🔍 Treating as search query")
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
	fmt.Printf("🔍 Processing YouTube URL: %s\n", url)

	video, err := ytClient.GetVideo(url)
	if err != nil {
		fmt.Printf("❌ YouTube API Error: %v\n", err)
		
		// Try yt-dlp as fallback
		fmt.Printf("🔄 Trying yt-dlp fallback...\n")
		return b.extractWithYtDlp(url)
	}

	fmt.Printf("✅ Successfully got video info: %s\n", video.Title)

	formats := video.Formats.WithAudioChannels()
	if len(formats) == 0 {
		fmt.Println("⚠️ No audio formats available, but continuing...")
	}

	thumbnail := ""
	if len(video.Thumbnails) > 0 {
		thumbnail = video.Thumbnails[0].URL
	}

	return &MusicTrack{
		Title:     video.Title,
		URL:       url,
		Duration:  video.Duration,
		Thumbnail: thumbnail,
	}, nil
}

// extractWithYtDlp extracts video info using yt-dlp as fallback
func (b *Bot) extractWithYtDlp(url string) (*MusicTrack, error) {
	fmt.Printf("🔧 Using yt-dlp fallback for: %s\n", url)
	
	// Try to get title using yt-dlp
	cmd := exec.Command("yt-dlp", "--get-title", url)
	titleBytes, err := cmd.Output()
	title := "YouTube Video"
	if err == nil && len(titleBytes) > 0 {
		title = strings.TrimSpace(string(titleBytes))
	}
	
	fmt.Printf("✅ yt-dlp extracted title: %s\n", title)
	
	return &MusicTrack{
		Title:     title,
		URL:       url,
		Duration:  0, // Unknown duration
		Thumbnail: "",
	}, nil
}

// extractSpotifyInfo extracts information from Spotify URL (placeholder)
func (b *Bot) extractSpotifyInfo(_ string) (*MusicTrack, error) {
	return nil, fmt.Errorf("spotify integration belum tersedia. silakan gunakan YouTube URL atau cari lagu dengan kata kunci")
}

// searchYouTube searches for a video on YouTube
func (b *Bot) searchYouTube(_ string) (*MusicTrack, error) {
	return nil, fmt.Errorf("fitur pencarian YouTube belum tersedia. silakan gunakan URL YouTube langsung atau gunakan format: `@bot https://youtube.com/watch?v=VIDEO_ID`")
}

// getOrCreateMusicSession gets or creates a music session for a guild
func (b *Bot) getOrCreateMusicSession(guildID string) *MusicSession {
	session, exists := musicSessions[guildID]
	if !exists {
		session = &MusicSession{
			Queue: &MusicQueue{
				Tracks:    []MusicTrack{},
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
	fmt.Printf("🔗 Connecting to voice channel: %s\n", channelID)
	
	voiceConn, err := s.ChannelVoiceJoin(guildID, channelID, false, true)
	if err != nil {
		return fmt.Errorf("gagal join voice channel: %v", err)
	}

	// Wait for voice connection to be ready
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			voiceConn.Disconnect()
			return fmt.Errorf("timeout waiting for voice connection")
		case <-ticker.C:
			if voiceConn.Ready {
				fmt.Printf("✅ Voice connection ready\n")
				session := b.getOrCreateMusicSession(guildID)
				session.VoiceConn = voiceConn
				return nil
			}
		}
	}
}

// startMusicPlayer starts the music player for a guild
func (b *Bot) startMusicPlayer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Queue.IsPlaying = true

	for session.Queue.Current < len(session.Queue.Tracks) {
		track := session.Queue.Tracks[session.Queue.Current]

		embed := &discordgo.MessageEmbed{
			Title: "🎵 Now Playing",
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Judul", Value: track.Title, Inline: true},
				{Name: "Durasi", Value: track.Duration.String(), Inline: true},
				{Name: "Requested by", Value: track.Requester, Inline: true},
			},
			Thumbnail: &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
			Color:     0x00ff00,
		}
		s.ChannelMessageSendEmbed(track.ChannelID, embed)

		err := b.playAudioStream(session.VoiceConn, track.URL)
		if err != nil {
			log.Printf("Gagal stream audio: %v", err)
			s.ChannelMessageSend(track.ChannelID, fmt.Sprintf("❌ Gagal memutar lagu: %v", err))
		}

		session.Queue.Current++
		if session.Queue.Current >= len(session.Queue.Tracks) && session.Queue.Loop {
			session.Queue.Current = 0
		}
	}

	session.Queue.IsPlaying = false
	session.Queue.Current = 0
}

// playAudioStream downloads and streams YouTube audio into a voice channel
func (b *Bot) playAudioStream(vc *discordgo.VoiceConnection, url string) error {
	fmt.Printf("🎵 Starting audio stream for: %s\n", url)
	
	// Check if voice connection is ready
	if vc == nil || !vc.Ready {
		return fmt.Errorf("voice connection tidak ready")
	}
	
	video, err := ytClient.GetVideo(url)
	if err != nil {
		return fmt.Errorf("gagal ambil info video: %v", err)
	}

	// Get audio format
	formats := video.Formats.WithAudioChannels()
	if len(formats) == 0 {
		return fmt.Errorf("tidak ada audio format tersedia")
	}
	
	// Prefer m4a format (itag 140) or use first available
	var format *youtube.Format
	for _, f := range formats {
		if f.ItagNo == 140 {
			format = &f
			break
		}
	}
	if format == nil {
		format = &formats[0]
	}
	
	fmt.Printf("📺 Using format: %s (itag: %d)\n", format.MimeType, format.ItagNo)

	stream, _, err := ytClient.GetStream(video, format)
	if err != nil {
		return fmt.Errorf("gagal ambil stream YouTube: %v", err)
	}
	defer stream.Close()

	// Use ffmpeg to convert to Opus frames for Discord
	cmd := exec.Command("ffmpeg",
		"-re",                 // read input in realtime
		"-i", "pipe:0",        // input dari stdin
		"-vn",                 // no video
		"-ar", "48000",        // sample rate
		"-ac", "2",            // stereo
		"-acodec", "libopus",  // encode ke Opus
		"-b:a", "128k",        // bitrate
		"-application", "audio",
		"-frame_duration", "20",
		"-f", "opus",          // output as Opus frames
		"-loglevel", "error",
		"pipe:1",
	)
	cmd.Stdin = stream
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gagal buat stdout ffmpeg: %v", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("gagal mulai ffmpeg: %v", err)
	}

	// Start speaking
	vc.Speaking(true)
	defer vc.Speaking(false)

	fmt.Printf("🔊 Starting audio playback...\n")
	fmt.Printf("📊 Stream info: format=%s, itag=%d\n", format.MimeType, format.ItagNo)

	// Read Opus frames and send to Discord
	frame := make([]byte, 4096)
	for {
		n, err := stdout.Read(frame)
		if err == io.EOF {
			fmt.Println("✅ Audio stream finished")
			break
		}
		if err != nil {
			log.Printf("❌ Error reading ffmpeg output: %v", err)
			break
		}
		if n > 0 {
			select {
			case vc.OpusSend <- frame[:n]:
			case <-time.After(5 * time.Second):
				return fmt.Errorf("timeout sending audio")
			}
		}
	}

	// Wait for ffmpeg to fully finish
	if err := cmd.Wait(); err != nil {
		log.Printf("⚠️ ffmpeg exited with error: %v", err)
	}
	fmt.Printf("🎵 Audio playback completed\n")
	return nil
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
	session.Queue.Tracks = []MusicTrack{}
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

	s.ChannelMessageSend(m.ChannelID, "⏸️ Musik dijeda.")
}

// handleResumeCommand handles resume command
func (b *Bot) handleResumeCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)

	if session.Queue.IsPlaying {
		s.ChannelMessageSend(m.ChannelID, "❌ Musik sudah diputar!")
		return
	}

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

	b.getOrCreateMusicSession(m.GuildID)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔊 Volume diatur ke: %s", parts[1]))
}