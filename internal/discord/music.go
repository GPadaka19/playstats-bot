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
	"strconv"
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
	Query     string
	Duration  time.Duration
	Requester string
	ChannelID string
	Thumbnail string
}

// MusicQueue represents a music queue for a guild
type MusicQueue struct {
	Tracks    []*MusicTrack // pointer slice
	IsPlaying bool
	Current   int
	Loop      bool
	Volume    float64
}

// MusicSession represents a music session for a guild
type MusicSession struct {
	Queue     *MusicQueue
	VoiceConn *discordgo.VoiceConnection

	// Kontrol Stream
	StreamCancel context.CancelFunc // Skip/Stop
	FfmpegProc   *os.Process        // Pause/Resume

	// Kontrol Idle
	IdleTimer *time.Timer

	// Mutex untuk thread safety
	Mu sync.Mutex
}

// Global variables
var (
	ytClient      = youtube.Client{}
	musicSessions = make(map[string]*MusicSession)
	sessionsMu    sync.Mutex
)

// handleMusicCommand handles music commands with bot mention
func (b *Bot) handleMusicCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	content := strings.TrimSpace(m.Content)
	botUserID := s.State.User.ID

	content = strings.ReplaceAll(content, "<@"+botUserID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botUserID+">", "")
	content = strings.TrimSpace(content)

	if content == "" {
		b.handleHelpCommand(s, m)
		return
	}

	// Cek voice state user (kecuali untuk help)
	if !strings.EqualFold(content, "help") {
		voiceState, err := s.State.VoiceState(m.GuildID, m.Author.ID)
		if err != nil || voiceState == nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Masuk voice channel dulu bang!")
			return
		}
	}

	parts := strings.Fields(content)
	if len(parts) == 0 {
		return
	}
	command := strings.ToLower(parts[0])

	switch command {
	case "help":
		b.handleHelpCommand(s, m)
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
		// Kalau bukan command spesifik, anggap user minta play lagu
		// Ambil channel ID dari voice state user terkini
		vs, _ := s.State.VoiceState(m.GuildID, m.Author.ID)
		if vs != nil {
			b.handlePlayMusic(s, m, content, vs.ChannelID)
		}
	}
}

// handleHelpCommand displays the help message
func (b *Bot) handleHelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	s.ChannelMessageSend(m.ChannelID, "🎵 **Music Bot Commands**\n"+
		"• `@bot [judul/URL]` - Play (Support Spotify Album/Playlist)\n"+
		"• `@bot skip` - Lewati lagu saat ini\n"+
		"• `@bot pause` / `resume` - Jeda atau lanjut lagu\n"+
		"• `@bot stop` - Stop & bersihkan queue (Auto-leave dalam 30d)\n"+
		"• `@bot leave` - Paksa bot keluar channel\n"+
		"• `@bot queue` - Lihat daftar antrian\n"+
		"• `@bot loop` - Aktifkan/matikan mode ulang\n"+
		"• `@bot help` - Tampilkan pesan ini")
}

// handlePlayMusic handles playing music logic
func (b *Bot) handlePlayMusic(s *discordgo.Session, m *discordgo.MessageCreate, query, channelID string) {
	loadingMsg, _ := s.ChannelMessageSend(m.ChannelID, "🔍 Mencari lagu...")

	tracks, err := b.extractMusicInfo(query)
	if err != nil {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, "❌ Error: "+err.Error())
		return
	}

	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()

	// Reset idle timer karena akan ada aktivitas
	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
		session.IdleTimer = nil
	}

	// Masukkan semua track ke queue
	for _, t := range tracks {
		t.Requester = m.Author.Username
		t.ChannelID = m.ChannelID
		session.Queue.Tracks = append(session.Queue.Tracks, t)
	}

	addedCount := len(tracks)
	queueCount := len(session.Queue.Tracks)
	session.Mu.Unlock()

	// Kirim respon (beda format untuk single vs playlist)
	if addedCount == 1 {
		t := tracks[0]
		title := t.Title
		if title == "" {
			title = t.Query
		} // Pakai query jika title belum resolve

		embed := &discordgo.MessageEmbed{
			Title:       "✅ Ditambahkan ke Queue",
			Description: fmt.Sprintf("[%s](%s)", title, t.URL),
			Fields: []*discordgo.MessageEmbedField{
				{Name: "Requester", Value: t.Requester, Inline: true},
				{Name: "Posisi", Value: fmt.Sprintf("#%d", queueCount), Inline: true},
			},
			Color: 0x00ff00,
		}
		if t.Thumbnail != "" {
			embed.Thumbnail = &discordgo.MessageEmbedThumbnail{URL: t.Thumbnail}
		}
		s.ChannelMessageEditEmbed(m.ChannelID, loadingMsg.ID, embed)
	} else {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, fmt.Sprintf("✅ **%d lagu** dari playlist/album ditambahkan ke antrian!", addedCount))
	}

	// Connect ke voice jika belum
	if session.VoiceConn == nil {
		if err := b.connectToVoice(s, m.GuildID, channelID); err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal connect voice: "+err.Error())
			return
		}
	}

	// Mulai player jika belum jalan
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
		// Cek apakah queue kosong
		if len(session.Queue.Tracks) == 0 || session.Queue.Current >= len(session.Queue.Tracks) {
			session.Queue.IsPlaying = false
			session.Mu.Unlock()

			// Queue selesai, mulai timer idle 30 detik
			b.startIdleTimer(s, guildID)
			return
		}

		track := session.Queue.Tracks[session.Queue.Current]

		if track.URL == "" {
			log.Printf("🔍 Lazy Loading: Mencari '%s' di YouTube...", track.Query)
			resolvedTrack, err := b.searchYouTube(track.Query)
			if err != nil {
				log.Printf("❌ Gagal resolve '%s': %v", track.Query, err)
				s.ChannelMessageSend(track.ChannelID, fmt.Sprintf("⚠️ Gagal memutar **%s**, skip...", track.Query))

				// Skip lagu error
				session.Queue.Current++
				session.Mu.Unlock()
				continue
			}
			// Update data track
			track.URL = resolvedTrack.URL
			track.Title = resolvedTrack.Title
			track.Duration = resolvedTrack.Duration
			track.Thumbnail = resolvedTrack.Thumbnail
		}

		// Setup Context untuk pembatalan (Skip/Stop)
		ctx, cancel := context.WithCancel(context.Background())
		session.StreamCancel = cancel
		session.Mu.Unlock()

		// Send Now Playing
		embed := &discordgo.MessageEmbed{
			Title:       "▶️ Now Playing",
			Description: fmt.Sprintf("[%s](%s)", track.Title, track.URL),
			Thumbnail:   &discordgo.MessageEmbedThumbnail{URL: track.Thumbnail},
			Color:       0x00ff00,
		}
		s.ChannelMessageSendEmbed(track.ChannelID, embed)

		// Play Audio (Blocking)
		err := b.playAudioStream(ctx, session, track.URL)
		if err != nil && err != context.Canceled {
			log.Printf("Playback Error: %v", err)
			s.ChannelMessageSend(track.ChannelID, "⚠️ Gagal memutar lagu, skip ke selanjutnya...")
		}

		// Cleanup
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

// startIdleTimer memulai timer 30 detik untuk keluar
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
		if session.Queue.IsPlaying {
			session.Mu.Unlock()
			return
		}

		conn := session.VoiceConn
		session.VoiceConn = nil
		session.IdleTimer = nil
		session.Queue.Tracks = nil
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

// playAudioStream streams audio (Direct Pipe yt-dlp -> ffmpeg)
func (b *Bot) playAudioStream(ctx context.Context, session *MusicSession, url string) error {
	if session.VoiceConn == nil {
		return fmt.Errorf("voice connection not ready")
	}

	fmt.Println("🔄 Mengambil stream URL via yt-dlp...")

	ytCmd := exec.CommandContext(ctx, "yt-dlp", "-f", "bestaudio", "-o", "-", "--quiet", url)
	ytOut, err := ytCmd.StdoutPipe()
	if err != nil {
		return err
	}

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

	if err := ytCmd.Start(); err != nil {
		return err
	}
	if err := ffmpegCmd.Start(); err != nil {
		return err
	}

	session.Mu.Lock()
	session.FfmpegProc = ffmpegCmd.Process
	session.Mu.Unlock()

	defer func() {
		if ytCmd.Process != nil {
			ytCmd.Process.Kill()
		}
		if ffmpegCmd.Process != nil {
			ffmpegCmd.Process.Kill()
		}
	}()

	encoder, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil {
		return err
	}

	session.VoiceConn.Speaking(true)
	defer session.VoiceConn.Speaking(false)

	frameSize := 960
	pcmBuf := make([]byte, frameSize*2*2)
	pcmInt16 := make([]int16, frameSize*2)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, err := io.ReadFull(ffmpegOut, pcmBuf)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// Baca data mentah (PCM)
		binary.Read(bytes.NewReader(pcmBuf), binary.LittleEndian, pcmInt16)

		// --- LOGIKA VOLUME DIMULAI ---
		session.Mu.Lock()
		vol := session.Queue.Volume // Ambil volume terkini
		session.Mu.Unlock()

		if vol != 1.0 {
			for i, v := range pcmInt16 {
				// Kalikan amplitudo suara dengan volume
				pcmInt16[i] = int16(float64(v) * vol)
			}
		}
		// --- LOGIKA VOLUME SELESAI ---

		opusData, err := encoder.Encode(pcmInt16, frameSize, frameSize*2)
		if err != nil {
			continue
		}

		select {
		case session.VoiceConn.OpusSend <- opusData:
		case <-time.After(1 * time.Second):
		}
	}
}

// --- Command Handlers ---

func (b *Bot) handleSkipCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	if session.StreamCancel != nil {
		session.StreamCancel()
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
		session.FfmpegProc.Signal(syscall.SIGSTOP)
		s.ChannelMessageSend(m.ChannelID, "⏸️ Paused.")
	}
}

func (b *Bot) handleResumeCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.FfmpegProc != nil {
		session.FfmpegProc.Signal(syscall.SIGCONT)
		s.ChannelMessageSend(m.ChannelID, "▶️ Resumed.")
	}
}

func (b *Bot) handleStopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	session.Queue.Tracks = nil
	session.Queue.Current = 0
	if session.StreamCancel != nil {
		session.StreamCancel()
	}
	s.ChannelMessageSend(m.ChannelID, "⏹️ Stopped. (Bot akan keluar dalam 30 detik jika idle)")
}

func (b *Bot) handleLeaveCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()

	session.Queue.Tracks = nil
	if session.StreamCancel != nil {
		session.StreamCancel()
	}
	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
	}

	conn := session.VoiceConn
	session.VoiceConn = nil
	session.Mu.Unlock()

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
		// PERBAIKAN: Gunakan make untuk slice pointer
		musicSessions[guildID] = &MusicSession{
			Queue: &MusicQueue{Tracks: make([]*MusicTrack, 0), Volume: 1.0},
		}
	}
	return musicSessions[guildID]
}

func (b *Bot) extractMusicInfo(query string) ([]*MusicTrack, error) {
	// Spotify Logic
	if b.isSpotifyURL(query) {
		if b.spotify == nil {
			return nil, fmt.Errorf("Spotify config missing")
		}

		// Dapatkan list query ("Artist - Title") dari Spotify Client
		queries, err := b.spotify.GetSpotifyTracks(query)
		if err != nil {
			return nil, err
		}

		var tracks []*MusicTrack
		for _, q := range queries {
			// Simpan Query saja, URL kosong (Lazy Load)
			tracks = append(tracks, &MusicTrack{
				Title: q,
				Query: q,
				URL:   "",
			})
		}
		return tracks, nil
	}

	// YouTube Logic
	track, err := b.searchYouTube(query)
	if err != nil {
		return nil, err
	}
	return []*MusicTrack{track}, nil
}

func (b *Bot) isYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

func (b *Bot) isSpotifyURL(url string) bool {
	return strings.Contains(url, "spotify.com") || strings.Contains(url, "open.spotify.com") || strings.Contains(url, "open.spotify.com")
}

func (b *Bot) extractSpotifyInfo(query string) ([]*MusicTrack, error) {
	// Pastikan client tidak nil
	if b.spotify == nil {
		return nil, fmt.Errorf("Spotify credentials belum diset di .env")
	}

	// Panggil fungsi BARU yang support playlist
	queries, err := b.spotify.GetSpotifyTracks(query)
	if err != nil {
		return nil, fmt.Errorf("gagal baca Spotify: %v", err)
	}

	var tracks []*MusicTrack
	for _, q := range queries {
		// Lazy Loading: Simpan Query saja, URL kosong
		tracks = append(tracks, &MusicTrack{
			Title: q,
			Query: q,
			URL:   "",
		})
	}
	return tracks, nil
}

func (b *Bot) searchYouTube(query string) (*MusicTrack, error) {
	// Cari ID, Judul, Durasi
	cmd := exec.Command("yt-dlp", "ytsearch1:"+query, "--print", "%(id)s\t%(title)s\t%(duration)s", "--no-playlist")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) < 2 {
		return nil, fmt.Errorf("not found")
	}

	durationSec := 0.0
	if len(parts) >= 3 {
		fmt.Sscanf(parts[2], "%f", &durationSec)
	}

	// Return Video Link asli (bukan stream link)
	return &MusicTrack{
		Title:     parts[1],
		URL:       "https://www.youtube.com/watch?v=" + parts[0],
		Duration:  time.Duration(durationSec) * time.Second,
		Thumbnail: "https://img.youtube.com/vi/" + parts[0] + "/hqdefault.jpg",
	}, nil
}

func (b *Bot) extractYouTubeInfo(url string) ([]*MusicTrack, error) {
	video, err := ytClient.GetVideo(url)
	if err != nil {
		return nil, err // Atau fallback extractWithYtDlp
	}
	thumb := ""
	if len(video.Thumbnails) > 0 {
		thumb = video.Thumbnails[0].URL
	}
	return []*MusicTrack{{Title: video.Title, URL: url, Duration: video.Duration, Thumbnail: thumb}}, nil
}

func (b *Bot) extractWithYtDlp(url string) (*MusicTrack, error) {
	cmd := exec.Command("yt-dlp", "--get-title", url)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
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
		if i == session.Queue.Current {
			state = "▶️ "
		}
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
	if session.Queue.Loop {
		state = "ON"
	}
	s.ChannelMessageSend(m.ChannelID, "🔁 Loop: "+state)
}

func (b *Bot) handleVolumeCommand(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "Format: `@bot volume [0-100]`")
		return
	}

	// Ubah text ke angka
	volInt, err := strconv.Atoi(parts[1])
	if err != nil || volInt < 0 || volInt > 100 {
		s.ChannelMessageSend(m.ChannelID, "❌ Masukkan angka antara 0 - 100.")
		return
	}

	// Simpan nilai volume ke session (Thread-safe)
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	session.Queue.Volume = float64(volInt) / 100.0 // Simpan sebagai float (misal 50 -> 0.5)
	session.Mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔊 Volume diatur ke **%d%%**", volInt))
}
