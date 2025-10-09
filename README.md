# PlayStats Bot

Bot Discord untuk melacak statistik voice dan aktivitas gaming.

## Commands

### Statistik Pribadi
- `!stats` - Statistik pribadi (voice + top 5 aktivitas)
- `!voice` - Waktu voice per channel
- `!play <game>` - Waktu bermain game tertentu

### Leaderboard
- `!leaderboard voice` - Top 10 voice di server
- `!leaderboard play <game>` - Top 10 game tertentu (global)

### Perbandingan
- `!compare @user1 @user2` - Bandingkan statistik dua user

### Laporan
- `!weekly` - Laporan mingguan
- `!monthly` - Laporan 4 minggu terakhir

### 🎵 Musik (Bot Mention)
- `@bot [judul lagu/YouTube URL]` - Memutar musik
- `@bot skip` - Melompati lagu saat ini
- `@bot stop` - Menghentikan musik dan membersihkan queue
- `@bot queue` - Menampilkan daftar lagu dalam queue
- `@bot pause` - Menjeda musik
- `@bot resume` - Melanjutkan musik
- `@bot loop` - Mengaktifkan/menonaktifkan mode loop
- `@bot volume [0-100]` - Mengatur volume
- `@bot` atau `@bot stats` - Menampilkan statistik (default)


## 🎯 Fitur Otomatis
Bot secara otomatis melacak:
- **Voice Activity**: Waktu di voice channel (per guild)
- **Game Activity**: Aktivitas bermain game/aplikasi (global)
- **Channel Activity**: Waktu di channel voice tertentu

## 📈 Database Schema
Bot menyimpan data di tabel:
- `voice_hours` - Total waktu voice per user per guild
- `activity_hours` - Total waktu aktivitas per user (global)
- `voice_channel_hours` - Waktu voice per channel per user
- `daily_stats` - Statistik harian (untuk reporting)
- `weekly_stats` - Statistik mingguan (untuk reporting)

## 🔧 Setup
1. Set environment variables:
   - `DISCORD_TOKEN` - Bot token dari Discord Developer Portal
   - `DATABASE_DSN` - PostgreSQL connection string

2. Jalankan bot:
   ```bash
   go run ./cmd/bot
   ```

3. Invite bot ke server dengan permissions:
   - Read Messages
   - Send Messages
   - Read Message History
   - View Channel
   - Connect (untuk voice tracking dan musik)
   - Speak (untuk musik)
   - View Server Members (untuk presence tracking)

## 🎵 Fitur Musik

Bot sekarang mendukung pemutaran musik dengan fitur:
- **YouTube Integration**: Memutar musik dari YouTube URL
- **Queue System**: Sistem antrian lagu per server
- **Voice Control**: Kontrol musik melalui voice channel
- **Rich Embeds**: Tampilan informasi lagu yang menarik

### Catatan Implementasi
- Fitur musik menggunakan mention bot (`@bot`) untuk membedakan dari command statistik
- Audio streaming memerlukan implementasi tambahan untuk actual playback
- Spotify integration memerlukan API key dan implementasi lebih lanjut