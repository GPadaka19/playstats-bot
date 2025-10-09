# ---------- Build stage ----------
    FROM golang:1.24.0-alpine AS builder
    WORKDIR /app
    
    # Install git (kadang diperlukan untuk go mod download)
    RUN apk add --no-cache git
    
    COPY . .
    RUN go mod tidy
    RUN go build -o bot ./cmd/bot
    
    # ---------- Runtime stage ----------
    FROM alpine:3.18
    WORKDIR /app
    
    # Install dependencies untuk audio processing
    RUN apk add --no-cache \
        ffmpeg \
        python3 \
        py3-pip \
        && pip3 install yt-dlp
    
    # Copy hasil build dari stage sebelumnya
    COPY --from=builder /app/bot .
    
    # Jalankan bot
    CMD ["./bot"]