package discord

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"layeh.com/gopus"

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

// Global variables
var (
	ytClient      = youtube.Client{}
	musicSessions = make(map[string]*MusicSession)
)

// handleMusicCommand handles music commands with bot mention
func (b *Bot) handleMusicCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	botUserID := s.State.User.ID

	// Clean up mention
	content = strings.ReplaceAll(content, "<@"+botUserID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botUserID+">", "")
	content = strings.TrimSpace(content)

	if content == "" {
		s.ChannelMessageSend(m.ChannelID, "🎵 **Music Bot Commands**\n"+
			"• `@bot [judul/URL]` - Play\n"+
			"• `@bot skip`, `stop`, `queue`, `pause`, `resume`\n"+
			"• `@bot loop`, `@bot volume [0-100]`")
		return
	}

	// Check voice state
	voiceState, err := s.State.VoiceState(m.GuildID, m.Author.ID)
	if err != nil || voiceState == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Masuk voice channel dulu bang!")
		return
	}

	// Parse command
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

// handlePlayMusic handles playing music logic
func (b *Bot) handlePlayMusic(s *discordgo.Session, m *discordgo.MessageCreate, query, channelID string) {
	loadingMsg, _ := s.ChannelMessageSend(m.ChannelID, "🔍 Mencari lagu...")

	track, err := b.extractMusicInfo(query)
	if err != nil {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, "❌ Error: "+err.Error())
		return
	}

	track.Requester = m.Author.Username
	track.ChannelID = m.ChannelID

	session := b.getOrCreateMusicSession(m.GuildID)
	session.Queue.Tracks = append(session.Queue.Tracks, *track)

	embed := &discordgo.MessageEmbed{
		Title:       "✅ Ditambahkan ke Queue",
		Description: fmt.Sprintf("[%s](%s)", track.Title, track.URL),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Durasi", Value: track.Duration.String(), Inline: true},
			{Name: "Requester", Value: track.Requester, Inline: true},
		},
		Thumbnail: &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
		Color:     0x00ff00,
	}
	s.ChannelMessageEditEmbed(m.ChannelID, loadingMsg.ID, embed)

	if session.VoiceConn == nil {
		if err := b.connectToVoice(s, m.GuildID, channelID); err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal connect voice: "+err.Error())
			return
		}
	}

	if !session.Queue.IsPlaying {
		go b.startMusicPlayer(s, m.GuildID)
	}
}

// extractMusicInfo determines source and extracts info
func (b *Bot) extractMusicInfo(query string) (*MusicTrack, error) {
	if b.isYouTubeURL(query) {
		return b.extractYouTubeInfo(query)
	}
	if b.isSpotifyURL(query) {
		return b.extractSpotifyInfo(query)
	}
	return b.searchYouTube(query)
}

// extractSpotifyInfo handles Spotify URLs
func (b *Bot) extractSpotifyInfo(url string) (*MusicTrack, error) {
	if b.spotify == nil {
		return nil, fmt.Errorf("Spotify credentials belum diset di .env")
	}
	searchQuery, err := b.spotify.GetTrackInfo(url)
	if err != nil {
		return nil, fmt.Errorf("gagal baca Spotify: %v", err)
	}
	return b.searchYouTube(searchQuery)
}

// searchYouTube searches video using yt-dlp
func (b *Bot) searchYouTube(query string) (*MusicTrack, error) {
	cmd := exec.Command("yt-dlp",
		"ytsearch1:"+query,
		"--print", "%(id)s\t%(title)s\t%(duration)s",
		"--no-playlist",
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pencarian gagal: %v", err)
	}

	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) < 2 {
		return nil, fmt.Errorf("lagu tidak ditemukan")
	}

	videoID := parts[0]
	title := parts[1]
	durationSec := 0.0
	if len(parts) >= 3 {
		fmt.Sscanf(parts[2], "%f", &durationSec)
	}

	return &MusicTrack{
		Title:     title,
		URL:       "https://www.youtube.com/watch?v=" + videoID,
		Duration:  time.Duration(durationSec) * time.Second,
		Thumbnail: "https://img.youtube.com/vi/" + videoID + "/hqdefault.jpg",
	}, nil
}

// extractYouTubeInfo gets info from direct YouTube URL
func (b *Bot) extractYouTubeInfo(url string) (*MusicTrack, error) {
	// Kita pakai Library CUMA untuk metadata (Title/Thumbnail) karena cepat
	// Tapi URL stream-nya nanti tetap pakai yt-dlp di playAudioStream
	video, err := ytClient.GetVideo(url)
	if err != nil {
		return b.extractWithYtDlp(url)
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

// extractWithYtDlp metadata fallback
func (b *Bot) extractWithYtDlp(url string) (*MusicTrack, error) {
	cmd := exec.Command("yt-dlp", "--get-title", url)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gagal mengambil info video")
	}
	return &MusicTrack{
		Title: strings.TrimSpace(string(out)),
		URL:   url,
	}, nil
}

// playAudioStream streams audio by piping yt-dlp output directly to ffmpeg
func (b *Bot) playAudioStream(vc *discordgo.VoiceConnection, url string) error {
	if vc == nil {
		return fmt.Errorf("voice connection not ready")
	}

	fmt.Println("🔄 Menggunakan strategi Direct Pipe (yt-dlp -> ffmpeg)...")

	// 1. Siapkan command yt-dlp untuk download ke STDOUT ("-o -")
	// Kita gunakan format bestaudio dan buffer yang cukup
	ytCmd := exec.Command("yt-dlp", 
		"-f", "bestaudio", 
		"-o", "-",      // Output ke stdout
		"--quiet",      // Jangan nyampah di log
		url,
	)

	// Ambil stdout dari yt-dlp (ini isinya data audio mentah)
	ytOut, err := ytCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gagal membuat pipe yt-dlp: %v", err)
	}

	// 2. Siapkan command ffmpeg untuk baca dari STDIN ("pipe:0")
	ffmpegCmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-i", "pipe:0", // Baca dari pipe (yt-dlp)
		"-f", "s16le",  // Format PCM untuk Discord
		"-ar", "48000", // 48kHz
		"-ac", "2",     // Stereo
		"pipe:1",       // Output ke stdout (untuk dibaca Go)
	)

	// Sambungkan output yt-dlp ke input ffmpeg
	ffmpegCmd.Stdin = ytOut

	// Ambil stdout dari ffmpeg (ini isinya PCM audio matang)
	ffmpegOut, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gagal membuat pipe ffmpeg: %v", err)
	}

	// 3. Jalankan kedua command
	if err := ytCmd.Start(); err != nil {
		return fmt.Errorf("gagal start yt-dlp: %v", err)
	}
	if err := ffmpegCmd.Start(); err != nil {
		return fmt.Errorf("gagal start ffmpeg: %v", err)
	}

	// Pastikan proses dibersihkan saat fungsi selesai
	defer func() {
		ytCmd.Process.Kill()
		ffmpegCmd.Process.Kill()
	}()

	// 4. Siapkan Encoder Opus
	encoder, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		return fmt.Errorf("gagal buat opus encoder: %v", err)
	}

	vc.Speaking(true)
	defer vc.Speaking(false)

	// Buffer setup (20ms frame)
	frameSize := 960
	pcmBuf := make([]byte, frameSize*2*2)
	pcmInt16 := make([]int16, frameSize*2)

	fmt.Println("▶️ Streaming dimulai...")

	for {
		// Baca data dari output FFmpeg
		_, err := io.ReadFull(ffmpegOut, pcmBuf)
		if err == io.EOF {
			fmt.Println("⏹️ Lagu selesai (EOF)")
			break
		}
		if err != nil {
			log.Printf("❌ Error reading ffmpeg stream: %v", err)
			break
		}

		// Convert bytes to int16
		if err := binary.Read(bytes.NewReader(pcmBuf), binary.LittleEndian, pcmInt16); err != nil {
			continue
		}

		// Encode ke Opus
		opusData, err := encoder.Encode(pcmInt16, frameSize, frameSize*2)
		if err != nil {
			continue
		}

		// Kirim ke Discord
		select {
		case vc.OpusSend <- opusData:
			// Berhasil kirim
		case <-time.After(1 * time.Second):
			log.Println("⚠️ Timeout sending opus packet")
			continue
		}
	}

	return nil
}

// startMusicPlayer Queue Loop
func (b *Bot) startMusicPlayer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Queue.IsPlaying = true

	for session.Queue.Current < len(session.Queue.Tracks) {
		track := session.Queue.Tracks[session.Queue.Current]

		s.ChannelMessageSendEmbed(track.ChannelID, &discordgo.MessageEmbed{
			Title:       "▶️ Now Playing",
			Description: track.Title,
			Thumbnail:   &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
			Color:       0x00ff00,
		})

		err := b.playAudioStream(session.VoiceConn, track.URL)
		if err != nil {
			log.Printf("Playback Error: %v", err)
			s.ChannelMessageSend(track.ChannelID, "⚠️ Gagal memutar lagu, skip ke selanjutnya...")
		}

		session.Queue.Current++
		if session.Queue.Current >= len(session.Queue.Tracks) {
			if session.Queue.Loop {
				session.Queue.Current = 0
			} else {
				break
			}
		}
	}

	session.Queue.IsPlaying = false
	session.Queue.Current = 0
	s.ChannelMessageSend(session.Queue.Tracks[0].ChannelID, "⏹️ Queue selesai.")
}

func (b *Bot) connectToVoice(s *discordgo.Session, guildID, channelID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vc, err := s.ChannelVoiceJoin(ctx, guildID, channelID, false, true)
	if err != nil {
		return fmt.Errorf("gagal join voice channel: %w", err)
	}

	session := b.getOrCreateMusicSession(guildID)
	session.VoiceConn = vc
	return nil
}

// --- Helper Functions ---

func (b *Bot) getOrCreateMusicSession(guildID string) *MusicSession {
	if _, ok := musicSessions[guildID]; !ok {
		musicSessions[guildID] = &MusicSession{
			Queue: &MusicQueue{
				Tracks: make([]MusicTrack, 0),
				Volume: 1.0,
			},
		}
	}
	return musicSessions[guildID]
}

func (b *Bot) isYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

func (b *Bot) isSpotifyURL(url string) bool {
	return strings.Contains(url, "spotify.com") || strings.Contains(url, "open.spotify.com")
}

// --- Simple Command Handlers ---

func (b *Bot) handleSkipCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	s.ChannelMessageSend(m.ChannelID, "⏭️ Skip command diterima.")
}

func (b *Bot) handleStopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Queue.Tracks = nil
	session.Queue.IsPlaying = false
	
	if session.VoiceConn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		session.VoiceConn.Disconnect(ctx)
		session.VoiceConn = nil
	}
	s.ChannelMessageSend(m.ChannelID, "⏹️ Stopped.")
}

func (b *Bot) handleQueueCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	if len(session.Queue.Tracks) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Queue kosong.")
		return
	}
	var msg strings.Builder
	msg.WriteString("**Queue:**\n")
	for i, t := range session.Queue.Tracks {
		state := " "
		if i == session.Queue.Current {
			state = "▶️ "
		}
		msg.WriteString(fmt.Sprintf("%s%d. %s\n", state, i+1, t.Title))
	}
	s.ChannelMessageSend(m.ChannelID, msg.String())
}

func (b *Bot) handlePauseCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	s.ChannelMessageSend(m.ChannelID, "⏸️ Paused (Placeholder).")
}

func (b *Bot) handleResumeCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	s.ChannelMessageSend(m.ChannelID, "▶️ Resumed (Placeholder).")
}

func (b *Bot) handleLoopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Queue.Loop = !session.Queue.Loop
	state := "OFF"
	if session.Queue.Loop {
		state = "ON"
	}
	s.ChannelMessageSend(m.ChannelID, "🔁 Loop: "+state)
}

func (b *Bot) handleVolumeCommand(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	s.ChannelMessageSend(m.ChannelID, "🔊 Volume changed (Placeholder).")
}