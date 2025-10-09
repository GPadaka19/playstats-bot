#!/bin/bash

echo "🔍 Simple test untuk yt-dlp dan ffmpeg..."

# Test URL
URL="https://youtu.be/KXbBBl5HGqo?si=Lx9MqxwKQaDpNipM"

echo "📺 Testing with URL: $URL"

# Test yt-dlp dengan full path
echo "1️⃣ Testing yt-dlp..."
~/.local/bin/yt-dlp --version

if [ $? -eq 0 ]; then
    echo "✅ yt-dlp is working"
    
    # Test audio extraction
    echo "2️⃣ Testing audio extraction..."
    ~/.local/bin/yt-dlp -f "bestaudio" --get-url "$URL"
    
    if [ $? -eq 0 ]; then
        echo "✅ Audio extraction successful"
    else
        echo "❌ Audio extraction failed"
    fi
else
    echo "❌ yt-dlp not working"
fi

# Test ffmpeg
echo "3️⃣ Testing ffmpeg..."
ffmpeg -version | head -1

if [ $? -eq 0 ]; then
    echo "✅ ffmpeg is working"
else
    echo "❌ ffmpeg not working"
fi

echo "🏁 Test completed"
