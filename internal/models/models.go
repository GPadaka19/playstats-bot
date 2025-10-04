package models

import "time"

// VoiceSession represents a user's voice channel session
type VoiceSession struct {
	Start     time.Time
	ChannelID string
}

// VoiceHours represents voice hours data in database
type VoiceHours struct {
	UserID       string
	GuildID      string
	TotalSeconds int64
}

// ActivityHours represents activity hours data in database
type ActivityHours struct {
	UserID       string
	ActivityName string
	TotalSeconds int64
}

// VoiceChannelHours represents voice channel hours data in database
type VoiceChannelHours struct {
	UserID       string
	GuildID      string
	ChannelID    string
	TotalSeconds int64
}
