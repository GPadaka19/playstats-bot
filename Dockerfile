# ---------- Build stage ----------
    FROM golang:1.24.0-alpine AS builder
    WORKDIR /app
    
    # Install git (kadang diperlukan untuk go mod download)
    RUN apk add --no-cache git gcc g++ musl-dev
    
    COPY . .
    RUN go mod tidy
    RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o bot ./cmd/bot
    
    # ---------- Runtime stage ----------
    FROM alpine:3.18
    WORKDIR /app
    
    # Install dependencies untuk audio processing & enkripsi voice
    RUN apk add --no-cache ffmpeg opus-dev libsodium-dev python3 py3-pip \
        && pip3 install --no-cache-dir yt-dlp
    
    # Copy hasil build dari stage sebelumnya
    COPY --from=builder /app/bot .
    
    # Jalankan bot
    CMD ["./bot"]