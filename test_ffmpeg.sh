#!/bin/bash

# Test script untuk memastikan ffmpeg bisa memproses YouTube audio
echo "🔍 Testing ffmpeg with YouTube audio..."

# Test URL
URL="https://youtu.be/KXbBBl5HGqo?si=Lx9MqxwKQaDpNipM"

echo "📺 Testing with URL: $URL"

# Test 1: Check if yt-dlp can extract audio URL
echo "1️⃣ Testing yt-dlp audio extraction..."

# Try different yt-dlp locations
YTDLP_CMD=""
if command -v yt-dlp &> /dev/null; then
    YTDLP_CMD="yt-dlp"
elif command -v ~/.local/bin/yt-dlp &> /dev/null; then
    YTDLP_CMD="~/.local/bin/yt-dlp"
elif [ -f ~/.local/bin/yt-dlp ]; then
    YTDLP_CMD="~/.local/bin/yt-dlp"
else
    echo "❌ yt-dlp not found in PATH or ~/.local/bin/"
    exit 1
fi

echo "📡 Using yt-dlp: $YTDLP_CMD"
$YTDLP_CMD -f "bestaudio[ext=m4a]" --get-url "$URL" 2>/dev/null

if [ $? -eq 0 ]; then
    echo "✅ yt-dlp can extract audio URL"
else
    echo "❌ yt-dlp failed to extract audio URL"
fi

# Test 2: Check ffmpeg with direct audio stream
echo "2️⃣ Testing ffmpeg with audio stream..."
AUDIO_URL=$($YTDLP_CMD -f "bestaudio[ext=m4a]" --get-url "$URL" 2>/dev/null)

if [ ! -z "$AUDIO_URL" ]; then
    echo "📡 Audio URL: $AUDIO_URL"
    
    # Test ffmpeg conversion
    timeout 10s ffmpeg -i "$AUDIO_URL" -f s16le -ar 48000 -ac 2 -t 5 -loglevel error pipe:1 > /dev/null
    
    if [ $? -eq 0 ]; then
        echo "✅ ffmpeg can process audio stream"
    else
        echo "❌ ffmpeg failed to process audio stream"
    fi
else
    echo "❌ Could not get audio URL"
fi

echo "🏁 Test completed"
