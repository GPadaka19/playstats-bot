#!/bin/bash

# Test script untuk memastikan ffmpeg bisa memproses YouTube audio
echo "🔍 Testing ffmpeg with YouTube audio..."

# Test URL
URL="https://youtu.be/KXbBBl5HGqo?si=Lx9MqxwKQaDpNipM"

echo "📺 Testing with URL: $URL"

# Test 1: Check if yt-dlp can extract audio URL
echo "1️⃣ Testing yt-dlp audio extraction..."
yt-dlp -f "bestaudio[ext=m4a]" --get-url "$URL" 2>/dev/null

if [ $? -eq 0 ]; then
    echo "✅ yt-dlp can extract audio URL"
else
    echo "❌ yt-dlp failed to extract audio URL"
fi

# Test 2: Check ffmpeg with direct audio stream
echo "2️⃣ Testing ffmpeg with audio stream..."
AUDIO_URL=$(yt-dlp -f "bestaudio[ext=m4a]" --get-url "$URL" 2>/dev/null)

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
