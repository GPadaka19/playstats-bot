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
    
    # Install ffmpeg untuk playback musik
    RUN apk add --no-cache ffmpeg
    
    # Copy hasil build dari stage sebelumnya
    COPY --from=builder /app/bot .
    
    # Jalankan bot
    CMD ["./bot"]