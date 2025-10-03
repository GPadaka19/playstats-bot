FROM golang:1.24.0-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod tidy
RUN go build -o bot main.go

FROM alpine:3.18
WORKDIR /app
COPY --from=builder /app/bot .
CMD ["./bot"]