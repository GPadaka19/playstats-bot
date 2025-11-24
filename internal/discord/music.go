package discord

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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
	Queue        *MusicQueue
	VoiceConn    *discordgo.VoiceConnection
	
	// Kontrol Stream
	StreamCancel context.CancelFunc // Untuk Skip/Stop
	FfmpegProc   *os.Process        // Untuk Pause/Resume
	
	// Kontrol Idle
	IdleTimer    *time.Timer
	
	// Mutex untuk thread safety
	Mu           sync.Mutex
}

// Global variables
var (
	ytClient      = youtube.Client{}
	musicSessions = make(map[string]*MusicSession)
	sessionsMu    sync.Mutex // Lock global map
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
			"• `@bot [judul/URL]` - Play Music\n"+
			"• `@bot skip` - Lewati lagu\n"+
			"• `@bot pause` / `resume` - Jeda/Lanjut\n"+
			"• `@bot stop` - Stop & Clear Queue (Auto-leave 30s)\n"+
			"• `@bot leave` - Paksa keluar sekarang\n"+
			"• `@bot queue` - Lihat antrian")
		return
	}

	// Check voice state
	voiceState, err := s.State.VoiceState(m.GuildID, m.Author.ID)
	if err != nil || voiceState == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Masuk voice channel dulu bang!")
		return
	}

	parts := strings.Fields(content)
	if len(parts) == 0 {
		return
	}
	command := strings.ToLower(parts[0])

	// Route commands
	switch command {
	case "skip":
		b.handleSkipCommand(s, m)
	case "stop":
		b.handleStopCommand(s, m)
	case "leave":
		b.handleLeaveCommand(s, m)
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
	
	// Lock session untuk update queue dan timer
	session.Mu.Lock()
	
	// Matikan idle timer jika ada karena kita mau main lagu
	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
		session.IdleTimer = nil
	}

	session.Queue.Tracks = append(session.Queue.Tracks, *track)
	session.Mu.Unlock()

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

	// Connect if needed
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

// startMusicPlayer Queue Loop
func (b *Bot) startMusicPlayer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Queue.IsPlaying = true

	for {
		session.Mu.Lock()
		// Cek apakah queue sudah habis atau indeks di luar batas
		if len(session.Queue.Tracks) == 0 || session.Queue.Current >= len(session.Queue.Tracks) {
			session.Queue.IsPlaying = false
			session.Mu.Unlock()
			
			// Queue selesai, mulai hitung mundur 30 detik untuk leave
			b.startIdleTimer(s, guildID)
			return
		}

		track := session.Queue.Tracks[session.Queue.Current]
		
		// Setup Context untuk pembatalan (Skip/Stop)
		ctx, cancel := context.WithCancel(context.Background())
		session.StreamCancel = cancel
		session.Mu.Unlock()

		// Send Now Playing
		s.ChannelMessageSendEmbed(track.ChannelID, &discordgo.MessageEmbed{
			Title:       "▶️ Now Playing",
			Description: track.Title,
			Thumbnail:   &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
			Color:       0x00ff00,
		})

		// Play Audio (Blocking sampai lagu selesai atau di-cancel)
		err := b.playAudioStream(ctx, session, track.URL)
		if err != nil && err != context.Canceled {
			log.Printf("Playback Error: %v", err)
			s.ChannelMessageSend(track.ChannelID, "⚠️ Gagal memutar lagu, skip ke selanjutnya...")
		}

		// Cleanup setelah lagu selesai
		session.Mu.Lock()
		session.StreamCancel = nil
		session.FfmpegProc = nil
		
		// Logic Next Track / Loop
		session.Queue.Current++
		if session.Queue.Current >= len(session.Queue.Tracks) {
			if session.Queue.Loop {
				session.Queue.Current = 0
			} else {
				// Selesai loop, akan break di iterasi berikutnya
			}
		}
		session.Mu.Unlock()
	}
}

// startIdleTimer memulai timer 30 detik untuk keluar dari channel
func (b *Bot) startIdleTimer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
	}

	// Set timer 30 detik
	session.IdleTimer = time.AfterFunc(30*time.Second, func() {
		session.Mu.Lock()
		// Cek lagi apakah sedang main lagu? (Jaga-jaga race condition)
		if session.Queue.IsPlaying {
			session.Mu.Unlock()
			return
		}
		
		conn := session.VoiceConn
		session.VoiceConn = nil
		session.IdleTimer = nil
		session.Queue.Tracks = nil // Reset queue
		session.Queue.Current = 0
		session.Mu.Unlock()

		if conn != nil {
			fmt.Printf("👋 Idle timeout 30s, leaving guild %s\n", guildID)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn.Disconnect(ctx)
		}
	})
}

// playAudioStream streams audio (Revised with Pause/Resume support)
func (b *Bot) playAudioStream(ctx context.Context, session *MusicSession, url string) error {
	if session.VoiceConn == nil {
		return fmt.Errorf("voice connection not ready")
	}

	fmt.Println("🔄 Mengambil stream URL via yt-dlp...")
	
	// 1. yt-dlp command
	ytCmd := exec.CommandContext(ctx, "yt-dlp", "-f", "bestaudio", "-o", "-", "--quiet", url)
	ytOut, err := ytCmd.StdoutPipe()
	if err != nil {
		return err
	}

	// 2. ffmpeg command
	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-i", "pipe:0",
		"-f", "s16le", "-ar", "48000", "-ac", "2",
		"pipe:1",
	)
	ffmpegCmd.Stdin = ytOut
	ffmpegOut, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		return err
	}

	// Start commands
	if err := ytCmd.Start(); err != nil {
		return err
	}
	if err := ffmpegCmd.Start(); err != nil {
		return err
	}

	// Simpan process FFmpeg ke session untuk fitur Pause
	session.Mu.Lock()
	session.FfmpegProc = ffmpegCmd.Process
	session.Mu.Unlock()

	defer func() {
		// Cleanup process
		if ytCmd.Process != nil { ytCmd.Process.Kill() }
		if ffmpegCmd.Process != nil { ffmpegCmd.Process.Kill() }
	}()

	// Opus Encoder
	encoder, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		return err
	}

	session.VoiceConn.Speaking(true)
	defer session.VoiceConn.Speaking(false)

	frameSize := 960
	pcmBuf := make([]byte, frameSize*2*2)
	pcmInt16 := make([]int16, frameSize*2)

	// Streaming Loop
	for {
		// Cek apakah context dibatalkan (Skip/Stop)
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Baca audio
		_, err := io.ReadFull(ffmpegOut, pcmBuf)
		if err == io.EOF {
			return nil // Lagu selesai normal
		}
		if err != nil {
			return err
		}

		// Encode
		if err := binary.Read(bytes.NewReader(pcmBuf), binary.LittleEndian, pcmInt16); err != nil {
			continue
		}
		opusData, err := encoder.Encode(pcmInt16, frameSize, frameSize*2)
		if err != nil {
			continue
		}

		// Kirim
		select {
		case session.VoiceConn.OpusSend <- opusData:
		case <-time.After(1 * time.Second):
			// Network lag? skip frame
		}
	}
}

// --- Command Handlers ---

func (b *Bot) handleSkipCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	if session.StreamCancel != nil {
		session.StreamCancel() // Batalkan context lagu saat ini -> Trigger next song
		s.ChannelMessageSend(m.ChannelID, "⏭️ Skipped.")
	} else {
		s.ChannelMessageSend(m.ChannelID, "❌ Tidak ada lagu yang sedang diputar.")
	}
}

func (b *Bot) handlePauseCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	if session.FfmpegProc != nil {
		// Kirim sinyal SIGSTOP (Pause process di Linux)
		if err := session.FfmpegProc.Signal(syscall.SIGSTOP); err == nil {
			s.ChannelMessageSend(m.ChannelID, "⏸️ Paused.")
		} else {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal pause (mungkin OS tidak support).")
		}
	}
}

func (b *Bot) handleResumeCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	if session.FfmpegProc != nil {
		// Kirim sinyal SIGCONT (Resume process di Linux)
		if err := session.FfmpegProc.Signal(syscall.SIGCONT); err == nil {
			s.ChannelMessageSend(m.ChannelID, "▶️ Resumed.")
		} else {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal resume.")
		}
	}
}

func (b *Bot) handleStopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	// Kosongkan queue
	session.Queue.Tracks = nil
	session.Queue.Current = 0
	
	// Matikan stream saat ini
	if session.StreamCancel != nil {
		session.StreamCancel()
	}
	
	s.ChannelMessageSend(m.ChannelID, "⏹️ Stopped. (Bot akan keluar dalam 30 detik jika idle)")
	// Idle timer akan otomatis berjalan karena startMusicPlayer loop akan selesai
}

func (b *Bot) handleLeaveCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	
	// Bersihkan semua
	session.Queue.Tracks = nil
	if session.StreamCancel != nil {
		session.StreamCancel()
	}
	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
	}
	
	conn := session.VoiceConn
	session.VoiceConn = nil
	session.Mu.Unlock() // Unlock sebelum disconnect agar tidak deadlock

	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Disconnect(ctx)
		s.ChannelMessageSend(m.ChannelID, "👋 Bye!")
	} else {
		s.ChannelMessageSend(m.ChannelID, "❌ Bot tidak di voice channel.")
	}
}

// --- Helper Functions ---

func (b *Bot) connectToVoice(s *discordgo.Session, guildID, channelID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vc, err := s.ChannelVoiceJoin(ctx, guildID, channelID, false, true)
	if err != nil {
		return fmt.Errorf("gagal join voice channel: %w", err)
	}

	session := b.getOrCreateMusicSession(guildID)
	session.Mu.Lock()
	session.VoiceConn = vc
	session.Mu.Unlock()
	return nil
}

func (b *Bot) getOrCreateMusicSession(guildID string) *MusicSession {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()

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

// --- Utilities & Other Commands (No Changes Needed for Logic, but included for completeness) ---

func (b *Bot) extractMusicInfo(query string) (*MusicTrack, error) {
	if b.isYouTubeURL(query) { return b.extractYouTubeInfo(query) }
	if b.isSpotifyURL(query) { return b.extractSpotifyInfo(query) }
	return b.searchYouTube(query)
}

func (b *Bot) isYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

func (b *Bot) isSpotifyURL(url string) bool {
	return strings.Contains(url, "spotify.com") || strings.Contains(url, "open.spotify.com")
}

func (b *Bot) extractSpotifyInfo(url string) (*MusicTrack, error) {
	if b.spotify == nil { return nil, fmt.Errorf("Spotify credentials missing") }
	searchQuery, err := b.spotify.GetTrackInfo(url)
	if err != nil { return nil, err }
	return b.searchYouTube(searchQuery)
}

func (b *Bot) searchYouTube(query string) (*MusicTrack, error) {
	cmd := exec.Command("yt-dlp", "ytsearch1:"+query, "--print", "%(id)s\t%(title)s\t%(duration)s", "--no-playlist")
	output, err := cmd.Output()
	if err != nil { return nil, err }
	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) < 2 { return nil, fmt.Errorf("not found") }
	
	durationSec := 0.0
	if len(parts) >= 3 { fmt.Sscanf(parts[2], "%f", &durationSec) }

	return &MusicTrack{
		Title: parts[1],
		URL: "https://www.youtube.com/watch?v=" + parts[0],
		Duration: time.Duration(durationSec) * time.Second,
		Thumbnail: "https://img.youtube.com/vi/" + parts[0] + "/hqdefault.jpg",
	}, nil
}

func (b *Bot) extractYouTubeInfo(url string) (*MusicTrack, error) {
	video, err := ytClient.GetVideo(url)
	if err != nil { return b.extractWithYtDlp(url) }
	thumb := ""
	if len(video.Thumbnails) > 0 { thumb = video.Thumbnails[0].URL }
	return &MusicTrack{Title: video.Title, URL: url, Duration: video.Duration, Thumbnail: thumb}, nil
}

func (b *Bot) extractWithYtDlp(url string) (*MusicTrack, error) {
	cmd := exec.Command("yt-dlp", "--get-title", url)
	out, err := cmd.Output()
	if err != nil { return nil, err }
	return &MusicTrack{Title: strings.TrimSpace(string(out)), URL: url}, nil
}

func (b *Bot) handleQueueCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if len(session.Queue.Tracks) == 0 {
		s.ChannelMessageSend(m.ChannelID, "Queue kosong.")
		return
	}
	var msg strings.Builder
	msg.WriteString("**Queue:**\n")
	for i, t := range session.Queue.Tracks {
		state := " "
		if i == session.Queue.Current { state = "▶️ " }
		msg.WriteString(fmt.Sprintf("%s%d. %s\n", state, i+1, t.Title))
	}
	s.ChannelMessageSend(m.ChannelID, msg.String())
}

func (b *Bot) handleLoopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()
	session.Queue.Loop = !session.Queue.Loop
	state := "OFF"
	if session.Queue.Loop { state = "ON" }
	s.ChannelMessageSend(m.ChannelID, "🔁 Loop: "+state)
}

func (b *Bot) handleVolumeCommand(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	s.ChannelMessageSend(m.ChannelID, "🔊 Volume changed (Placeholder).")
}