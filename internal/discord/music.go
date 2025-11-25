package discord

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"layeh.com/gopus"

	"github.com/bwmarrin/discordgo"
	"github.com/kkdai/youtube/v2"
)


type LyricsResult struct {
	PlainLyrics string `json:"plainLyrics"`
	TrackName   string `json:"trackName"`
	ArtistName  string `json:"artistName"`
}

type MusicTrack struct {
	Title     string
	URL       string
	Duration  time.Duration
	Requester string
	ChannelID string
	Thumbnail string
}

type MusicQueue struct {
	Tracks    []*MusicTrack
	IsPlaying bool
	Current   int
	Loop      bool
	Volume    float64
}

type MusicSession struct {
	Queue        *MusicQueue
	VoiceConn    *discordgo.VoiceConnection
	StreamCancel context.CancelFunc
	FfmpegProc   *os.Process
	IdleTimer    *time.Timer
	Mu           sync.Mutex
}

var (
	ytClient      = youtube.Client{}
	musicSessions = make(map[string]*MusicSession)
	searchSessions = make(map[string][]*MusicTrack)
	sessionsMu    sync.Mutex
	searchMu      sync.Mutex
)

// --- HANDLER UTAMA ---
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

	parts := strings.Fields(content)
	command := ""
	if len(parts) > 0 {
		command = strings.ToLower(parts[0])
	}

	if command != "help" && command != "h" {
		voiceState, err := s.State.VoiceState(m.GuildID, m.Author.ID)
		if err != nil || voiceState == nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Masuk voice channel dulu bang!")
			return
		}
	}

	switch command {
	case "help", "h": // Alias h
		b.handleHelpCommand(s, m)
	case "skip":
		b.handleSkipCommand(s, m)
	case "stop":
		b.handleStopCommand(s, m)
	case "leave", "l":
		b.handleLeaveCommand(s, m)
	case "queue", "q": // Alias q
		b.handleQueueCommand(s, m)
	case "pause":
		b.handlePauseCommand(s, m)
	case "resume":
		b.handleResumeCommand(s, m)
	case "loop":
		b.handleLoopCommand(s, m)
	case "volume":
		b.handleVolumeCommand(s, m, parts)
	case "lyrics", "ly":
		b.handleLyricsCommand(s, m)
	case "search", "s":
		b.handleSearchCommand(s, m, parts)
	default:
		b.handlePlayMusic(s, m, content, "")
	}
}

func (b *Bot) handleSearchCommand(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Format: `@bot search [judul lagu]`")
		return
	}
	query := strings.Join(parts[1:], " ")
	s.ChannelMessageSend(m.ChannelID, "🔍 Mencari 10 lagu teratas...")

	// Cari 10 lagu via yt-dlp
	tracks, err := b.searchYouTubeList(query)
	if err != nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Gagal mencari: "+err.Error())
		return
	}

	// Simpan session pencarian user ini
	searchMu.Lock()
	searchSessions[m.Author.ID] = tracks
	searchMu.Unlock()

	// Tampilkan Embed
	var desc strings.Builder
	for i, t := range tracks {
		desc.WriteString(fmt.Sprintf("`%d.` %s (%s)\n", i+1, t.Title, t.Duration))
	}
	desc.WriteString("\n**Ketik angka 1-10 untuk memilih, atau `cancel` untuk batal.**")

	s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
		Title:       "🔍 Hasil Pencarian: " + query,
		Description: desc.String(),
		Color:       0x00ff00,
	})

	// Hapus session setelah 30 detik (Timeout)
	time.AfterFunc(30*time.Second, func() {
		searchMu.Lock()
		if _, ok := searchSessions[m.Author.ID]; ok {
			delete(searchSessions, m.Author.ID)
		}
		searchMu.Unlock()
	})
}

// Fungsi ini dipanggil oleh bot.go jika user mengetik angka
func (b *Bot) handleSearchSelection(s *discordgo.Session, m *discordgo.MessageCreate) bool {
	searchMu.Lock()
	tracks, ok := searchSessions[m.Author.ID]
	searchMu.Unlock()

	if !ok { return false } // Bukan response search

	content := strings.TrimSpace(m.Content)
	
	if strings.ToLower(content) == "cancel" {
		searchMu.Lock()
		delete(searchSessions, m.Author.ID)
		searchMu.Unlock()
		s.ChannelMessageSend(m.ChannelID, "❌ Pencarian dibatalkan.")
		return true
	}

	// Cek apakah angka valid
	choice, err := strconv.Atoi(content)
	if err != nil || choice < 1 || choice > len(tracks) {
		// Jika user ngetik chat biasa, abaikan saja (biar tidak ganggu)
		return false
	}

	// Pilih lagu
	selected := tracks[choice-1]
	
	// Bersihkan session
	searchMu.Lock()
	delete(searchSessions, m.Author.ID)
	searchMu.Unlock()

	// Mainkan lagu (Mirip logika handlePlayMusic tapi dipotong)
	session := b.getOrCreateMusicSession(m.GuildID)
	selected.Requester = m.Author.Username
	selected.ChannelID = m.ChannelID
	
	// Cek voice state untuk ambil channel ID (karena handleSearchCommand tidak ngecek voice)
	vs, _ := s.State.VoiceState(m.GuildID, m.Author.ID)
	if vs == nil {
		s.ChannelMessageSend(m.ChannelID, "❌ Masuk voice channel dulu bang!")
		return true
	}

	session.Mu.Lock()
	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
		session.IdleTimer = nil
	}
	session.Queue.Tracks = append(session.Queue.Tracks, selected)
	queuePos := len(session.Queue.Tracks)
	session.Mu.Unlock()

	s.ChannelMessageSendEmbed(m.ChannelID, &discordgo.MessageEmbed{
		Title:       "✅ Ditambahkan ke Queue",
		Description: fmt.Sprintf("[%s](%s)", selected.Title, selected.URL),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Posisi", Value: fmt.Sprintf("#%d", queuePos), Inline: true},
		},
		Color:       0x00ff00,
		Thumbnail:   &discordgo.MessageEmbedThumbnail{URL: selected.Thumbnail},
	})

	if session.VoiceConn == nil {
		if err := b.connectToVoice(s, m.GuildID, vs.ChannelID); err != nil {
			s.ChannelMessageSend(m.ChannelID, "❌ Gagal connect voice: "+err.Error())
			return true
		}
	}

	if !session.Queue.IsPlaying {
		go b.startMusicPlayer(s, m.GuildID)
	}

	return true // Handled
}

func (b *Bot) searchYouTubeList(query string) ([]*MusicTrack, error) {
	// ytsearch10: cari 10 hasil
	cmd := exec.Command("yt-dlp", "ytsearch10:"+query, "--print", "%(id)s\t%(title)s\t%(duration)s", "--no-playlist")
	output, err := cmd.Output()
	if err != nil { return nil, err }

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var tracks []*MusicTrack
	
	for _, line := range lines {
		if line == "" { continue }
		parts := strings.Split(line, "\t")
		if len(parts) < 2 { continue }
		
		durationSec := 0.0
		if len(parts) >= 3 { fmt.Sscanf(parts[2], "%f", &durationSec) }

		tracks = append(tracks, &MusicTrack{
			Title:    parts[1],
			URL:      "https://www.youtube.com/watch?v=" + parts[0], // Original URL
			Duration: time.Duration(durationSec) * time.Second,
			Thumbnail: "https://img.youtube.com/vi/" + parts[0] + "/hqdefault.jpg",
		})
	}
	return tracks, nil
}

// --- FITUR LIRIK ---

func (b *Bot) handleLyricsCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	// Cek apakah ada lagu yang sedang diputar
	if len(session.Queue.Tracks) == 0 || session.Queue.Current >= len(session.Queue.Tracks) {
		session.Mu.Unlock()
		s.ChannelMessageSend(m.ChannelID, "❌ Tidak ada lagu yang sedang diputar.")
		return
	}
	currentTrack := session.Queue.Tracks[session.Queue.Current]
	session.Mu.Unlock()

	loadingMsg, _ := s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔍 Mencari lirik untuk: **%s**...", currentTrack.Title))

	// Bersihkan judul lagu agar pencarian lebih akurat
	// Hapus (Official Video), [Lyrics], dll
	cleanTitle := cleanTrackTitle(currentTrack.Title)
	
	lyrics, err := fetchLyrics(cleanTitle)
	if err != nil {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, "❌ Maaf, lirik tidak ditemukan.")
		return
	}

	// Kirim Lirik (Potong jika terlalu panjang untuk Embed)
	if len(lyrics) > 4000 {
		lyrics = lyrics[:3990] + "..."
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🎤 Lirik: " + currentTrack.Title,
		Description: lyrics,
		Color:       0x3498db, // Biru
		Footer:      &discordgo.MessageEmbedFooter{Text: "Source: Lrclib.net"},
	}
	
	s.ChannelMessageDelete(m.ChannelID, loadingMsg.ID)
	s.ChannelMessageSendEmbed(m.ChannelID, embed)
}

func fetchLyrics(query string) (string, error) {
	// API Request ke Lrclib
	apiURL := fmt.Sprintf("https://lrclib.net/api/search?q=%s", url.QueryEscape(query))
	resp, err := http.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var results []LyricsResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "", fmt.Errorf("not found")
	}

	// Ambil hasil pertama
	return results[0].PlainLyrics, nil
}

func cleanTrackTitle(title string) string {
	// Regex untuk menghapus teks sampah di judul YouTube
	// Contoh: "Linkin Park - Numb (Official Video)" -> "Linkin Park - Numb"
	re := regexp.MustCompile(`(?i)(\(.*\)|\[.*\]|official|video|audio|lyrics|lyric|mv|music video|hd|4k)`)
	clean := re.ReplaceAllString(title, "")
	return strings.TrimSpace(clean)
}

// --- LOGIC PLAYLIST & PLAY ---
func (b *Bot) handlePlayMusic(s *discordgo.Session, m *discordgo.MessageCreate, query, channelID string) {
	loadingMsg, _ := s.ChannelMessageSend(m.ChannelID, "🔍 Memproses permintaan...")

	// 1. Extract Info
	tracks, err := b.extractMusicInfo(query)
	if err != nil {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, "❌ Error: "+err.Error())
		return
	}

	// 2. Set Info Tambahan
	if channelID == "" {
		vs, _ := s.State.VoiceState(m.GuildID, m.Author.ID)
		if vs != nil { channelID = vs.ChannelID }
	}

	for _, t := range tracks {
		t.Requester = m.Author.Username
		t.ChannelID = channelID
	}

	// 3. Masukkan ke Queue
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	if session.IdleTimer != nil {
		session.IdleTimer.Stop()
		session.IdleTimer = nil
	}
	session.Queue.Tracks = append(session.Queue.Tracks, tracks...)
	session.Mu.Unlock()

	// 4. Response
	if len(tracks) == 1 {
		embed := &discordgo.MessageEmbed{
			Title:       "✅ Ditambahkan ke Queue",
			Description: fmt.Sprintf("[%s](%s)", tracks[0].Title, tracks[0].URL),
			Color:       0x00ff00,
		}
		s.ChannelMessageEditEmbed(m.ChannelID, loadingMsg.ID, embed)
	} else {
		s.ChannelMessageEdit(m.ChannelID, loadingMsg.ID, fmt.Sprintf("✅ Berhasil menambahkan **%d** lagu dari Playlist ke Queue!", len(tracks)))
	}

	// 5. Connect & Play
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

func (b *Bot) extractMusicInfo(query string) ([]*MusicTrack, error) {
	if b.isYouTubeURL(query) {
		t, err := b.extractYouTubeInfo(query)
		if err != nil { return nil, err }
		return []*MusicTrack{t}, nil
	}
	if b.isSpotifyURL(query) {
		return b.extractSpotifyInfo(query)
	}
	t, err := b.searchYouTube(query)
	if err != nil { return nil, err }
	return []*MusicTrack{t}, nil
}

func (b *Bot) extractSpotifyInfo(url string) ([]*MusicTrack, error) {
	if b.spotify == nil { return nil, fmt.Errorf("Spotify credentials missing") }
	
	// PERBAIKAN: Memanggil GetSpotifyInfo (bukan GetSpotifyTracks)
	titles, err := b.spotify.GetSpotifyInfo(url)
	if err != nil { return nil, err }

	var tracks []*MusicTrack
	for _, title := range titles {
		tracks = append(tracks, &MusicTrack{
			Title: title,
			URL:   "ytsearch:" + title, // Transformasi penting: String -> Search Query
		})
	}
	return tracks, nil
}

// --- PLAYER ENGINE (ANTI-EOF) ---

func (b *Bot) startMusicPlayer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Queue.IsPlaying = true

	for {
		session.Mu.Lock()
		if len(session.Queue.Tracks) == 0 || session.Queue.Current >= len(session.Queue.Tracks) {
			session.Queue.IsPlaying = false
			session.Mu.Unlock()
			b.startIdleTimer(s, guildID)
			return
		}

		track := session.Queue.Tracks[session.Queue.Current]
		ctx, cancel := context.WithCancel(context.Background())
		session.StreamCancel = cancel
		session.Mu.Unlock()

		s.ChannelMessageSendEmbed(track.ChannelID, &discordgo.MessageEmbed{
			Title:       "▶️ Now Playing",
			Description: track.Title,
			Color:       0x00ff00,
		})

		err := b.playAudioStream(ctx, session, track.URL)
		if err != nil && err != context.Canceled {
			log.Printf("Playback Error: %v", err)
			s.ChannelMessageSend(track.ChannelID, "⚠️ Gagal memutar, skip...")
		}

		session.Mu.Lock()
		session.StreamCancel = nil
		session.FfmpegProc = nil
		session.Queue.Current++
		if session.Queue.Current >= len(session.Queue.Tracks) {
			if session.Queue.Loop { session.Queue.Current = 0 }
		}
		session.Mu.Unlock()
	}
}

func (b *Bot) playAudioStream(ctx context.Context, session *MusicSession, url string) error {
	if session.VoiceConn == nil { return fmt.Errorf("voice connection not ready") }

	fmt.Println("🔄 Mengambil stream URL via yt-dlp (Direct Pipe)...")
	
	// JURUS PAMUNGKAS: Direct Pipe yt-dlp -> ffmpeg
	ytCmd := exec.CommandContext(ctx, "yt-dlp", "-f", "bestaudio", "-o", "-", "--quiet", url)
	ytOut, err := ytCmd.StdoutPipe()
	if err != nil { return err }

	ffmpegCmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", "pipe:0", "-f", "s16le", "-ar", "48000", "-ac", "2", "pipe:1")
	ffmpegCmd.Stdin = ytOut
	ffmpegOut, err := ffmpegCmd.StdoutPipe()
	if err != nil { return err }

	if err := ytCmd.Start(); err != nil { return err }
	if err := ffmpegCmd.Start(); err != nil { return err }

	session.Mu.Lock()
	session.FfmpegProc = ffmpegCmd.Process
	session.Mu.Unlock()

	defer func() {
		if ytCmd.Process != nil { ytCmd.Process.Kill() }
		if ffmpegCmd.Process != nil { ffmpegCmd.Process.Kill() }
	}()

	encoder, err := gopus.NewEncoder(48000, 2, gopus.Audio)
	if err != nil { return err }

	session.VoiceConn.Speaking(true)
	defer session.VoiceConn.Speaking(false)

	frameSize := 960
	pcmBuf := make([]byte, frameSize*2*2)
	pcmInt16 := make([]int16, frameSize*2)

	for {
		select {
		case <-ctx.Done(): return ctx.Err()
		default:
		}
		_, err := io.ReadFull(ffmpegOut, pcmBuf)
		if err == io.EOF { return nil }
		if err != nil { return err }
		binary.Read(bytes.NewReader(pcmBuf), binary.LittleEndian, pcmInt16)
		opusData, _ := encoder.Encode(pcmInt16, frameSize, frameSize*2)
		
		select {
		case session.VoiceConn.OpusSend <- opusData:
		case <-time.After(1 * time.Second):
		}
	}
}

// --- COMMANDS HELPERS & UTILS ---

func (b *Bot) handleHelpCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	s.ChannelMessageSend(m.ChannelID, "🎵 **Music Bot Commands**\n"+
		"• `@bot [judul/URL]` - Putar lagu\n"+
		"• `@bot q` / `queue` - Lihat antrian\n"+
		"• `@bot l` / `lyrics` - Lihat lirik lagu saat ini\n"+
		"• `@bot skip` - Lewati lagu\n"+
		"• `@bot pause` / `resume` - Kontrol playback\n"+
		"• `@bot vol [0-100]` - Atur volume\n"+
		"• `@bot loop` - Mode ulang\n"+
		"• `@bot stop` - Stop & Clear\n"+
		"• `@bot leave` - Keluar channel")
}

func (b *Bot) handleSkipCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.StreamCancel != nil {
		session.StreamCancel()
		s.ChannelMessageSend(m.ChannelID, "⏭️ Skipped.")
	}
}

func (b *Bot) handleLeaveCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
    session := b.getOrCreateMusicSession(m.GuildID)
    session.Mu.Lock()
    
    session.Queue.Tracks = nil
    if session.StreamCancel != nil { session.StreamCancel() }
    if session.IdleTimer != nil { session.IdleTimer.Stop() }
    
    conn := session.VoiceConn
    session.VoiceConn = nil
    session.Mu.Unlock()

    // PERBAIKAN: Cek apakah koneksi ada, bukan cek command string
    if conn != nil {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        conn.Disconnect(ctx)
        s.ChannelMessageSend(m.ChannelID, "👋 Bye!")
    } else {
        s.ChannelMessageSend(m.ChannelID, "❌ Bot tidak di voice channel.")
    }
}

func (b *Bot) handleStopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	session.Queue.Tracks = nil
	session.Queue.Current = 0
	if session.StreamCancel != nil { session.StreamCancel() }
	session.Mu.Unlock()
	s.ChannelMessageSend(m.ChannelID, "⏹️ Stopped.")
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

func (b *Bot) handleQueueCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()

	if len(session.Queue.Tracks) == 0 {
		s.ChannelMessageSend(m.ChannelID, "📭 Queue kosong.")
		return
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("🎵 **Music Queue** (Total: %d)\n\n", len(session.Queue.Tracks)))

	// Tentukan range lagu yang ditampilkan (agar tidak kena limit 2000 char)
	// Kita tampilkan dari 2 lagu sebelumnya sampai 15 lagu ke depan
	start := session.Queue.Current - 2
	if start < 0 {
		start = 0
	}
	
	end := start + 15
	if end > len(session.Queue.Tracks) {
		end = len(session.Queue.Tracks)
	}

	if start > 0 {
		msg.WriteString(fmt.Sprintf("... (ada %d lagu sebelumnya)\n", start))
	}

	for i := start; i < end; i++ {
		t := session.Queue.Tracks[i]
		
		state := "   " // Spasi indentasi
		if i == session.Queue.Current {
			state = "▶️ " // Penanda lagu aktif
		}

		// Truncate judul jika terlalu panjang (>45 char) agar rapi
		title := t.Title
		if len(title) > 45 {
			title = title[:42] + "..."
		}
		
		// Tambahkan info durasi jika ada
		duration := "Live"
		if t.Duration > 0 {
			duration = t.Duration.String()
		}

		msg.WriteString(fmt.Sprintf("`%s%d.` %s | %s\n", state, i+1, title, duration))
	}

	remaining := len(session.Queue.Tracks) - end
	if remaining > 0 {
		msg.WriteString(fmt.Sprintf("\n... dan **%d** lagu lainnya.", remaining))
	}

	s.ChannelMessageSend(m.ChannelID, msg.String())
}

func (b *Bot) handleLoopCommand(s *discordgo.Session, m *discordgo.MessageCreate) {
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()
	session.Queue.Loop = !session.Queue.Loop
	state := "OFF"; if session.Queue.Loop { state = "ON" }
	s.ChannelMessageSend(m.ChannelID, "🔁 Loop: "+state)
}

func (b *Bot) handleVolumeCommand(s *discordgo.Session, m *discordgo.MessageCreate, parts []string) {
	if len(parts) < 2 {
		s.ChannelMessageSend(m.ChannelID, "❌ Format: `@bot volume [0-100]`")
		return
	}

	// 1. Parsing angka
	vol, err := strconv.Atoi(parts[1])
	if err != nil || vol < 0 || vol > 100 {
		s.ChannelMessageSend(m.ChannelID, "❌ Masukkan angka volume antara 0 sampai 100.")
		return
	}

	// 2. Simpan ke Session
	session := b.getOrCreateMusicSession(m.GuildID)
	session.Mu.Lock()
	session.Queue.Volume = float64(vol) / 100.0 // Ubah jadi float 0.0 - 1.0
	session.Mu.Unlock()

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("🔊 Volume diatur ke **%d%%**", vol))
}

func (b *Bot) startIdleTimer(s *discordgo.Session, guildID string) {
	session := b.getOrCreateMusicSession(guildID)
	session.Mu.Lock()
	defer session.Mu.Unlock()
	if session.IdleTimer != nil { session.IdleTimer.Stop() }
	session.IdleTimer = time.AfterFunc(30*time.Second, func() {
		session.Mu.Lock()
		if session.Queue.IsPlaying { session.Mu.Unlock(); return }
		conn := session.VoiceConn
		session.VoiceConn = nil
		session.Mu.Unlock()
		if conn != nil {
			ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
			conn.Disconnect(ctx)
		}
	})
}

func (b *Bot) getOrCreateMusicSession(guildID string) *MusicSession {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if _, ok := musicSessions[guildID]; !ok {
		musicSessions[guildID] = &MusicSession{Queue: &MusicQueue{Tracks: make([]*MusicTrack, 0), Volume: 1.0}}
	}
	return musicSessions[guildID]
}

func (b *Bot) connectToVoice(s *discordgo.Session, guildID, channelID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vc, err := s.ChannelVoiceJoin(ctx, guildID, channelID, false, true)
	if err != nil { return err }
	session := b.getOrCreateMusicSession(guildID)
	session.Mu.Lock()
	session.VoiceConn = vc
	session.Mu.Unlock()
	return nil
}

func (b *Bot) isYouTubeURL(url string) bool { return strings.Contains(url, "youtu") }
func (b *Bot) isSpotifyURL(url string) bool { return strings.Contains(url, "spotify") }

func (b *Bot) searchYouTube(query string) (*MusicTrack, error) {
	return &MusicTrack{Title: query, URL: "ytsearch:" + query}, nil
}

func (b *Bot) extractYouTubeInfo(url string) (*MusicTrack, error) {
	return &MusicTrack{Title: "YouTube Video", URL: url}, nil
}

func (b *Bot) extractWithYtDlp(url string) (*MusicTrack, error) {
	return &MusicTrack{Title: "Video", URL: url}, nil
}