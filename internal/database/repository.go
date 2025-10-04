package database

import (
	"database/sql"
	"fmt"
	"log"
)

// Repository handles database operations
type Repository struct {
	db *DB
}

// NewRepository creates a new repository
func NewRepository(db *DB) *Repository {
	return &Repository{db: db}
}

// AddVoiceSeconds adds voice seconds to the database
func (r *Repository) AddVoiceSeconds(userID, guildID string, seconds int64) error {
	_, err := r.db.conn.Exec(`
		INSERT INTO voice_hours (user_id, guild_id, total_seconds)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, guild_id) DO UPDATE SET total_seconds = voice_hours.total_seconds + EXCLUDED.total_seconds`,
		userID, guildID, seconds)
	if err != nil {
		return fmt.Errorf("failed to add voice seconds: %w", err)
	}
	return nil
}

// AddActivitySeconds adds activity seconds to the database
func (r *Repository) AddActivitySeconds(userID, activityName string, seconds int64) error {
	_, err := r.db.conn.Exec(`
		INSERT INTO activity_hours (user_id, activity_name, total_seconds)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, activity_name) DO UPDATE SET total_seconds = activity_hours.total_seconds + EXCLUDED.total_seconds`,
		userID, activityName, seconds)
	if err != nil {
		return fmt.Errorf("failed to add activity seconds: %w", err)
	}
	return nil
}

// AddChannelSeconds adds voice channel seconds to the database
func (r *Repository) AddChannelSeconds(userID, guildID, channelID string, seconds int64) error {
	_, err := r.db.conn.Exec(`
		INSERT INTO voice_channel_hours (user_id, guild_id, channel_id, total_seconds)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, guild_id, channel_id) DO UPDATE SET total_seconds = voice_channel_hours.total_seconds + EXCLUDED.total_seconds`,
		userID, guildID, channelID, seconds)
	if err != nil {
		return fmt.Errorf("failed to add channel seconds: %w", err)
	}
	return nil
}

// GetVoiceHours gets total voice hours for a user in a guild
func (r *Repository) GetVoiceHours(userID, guildID string) (int64, error) {
	var totalSeconds int64
	err := r.db.conn.QueryRow(
		"SELECT total_seconds FROM voice_hours WHERE user_id = $1 AND guild_id = $2",
		userID, guildID).Scan(&totalSeconds)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to get voice hours: %w", err)
	}
	return totalSeconds, nil
}

// GetActivityHours gets total activity hours for a user and activity
func (r *Repository) GetActivityHours(userID, activityName string) (int64, error) {
	var totalSeconds int64
	err := r.db.conn.QueryRow(
		"SELECT total_seconds FROM activity_hours WHERE user_id = $1 AND activity_name = $2",
		userID, activityName).Scan(&totalSeconds)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to get activity hours: %w", err)
	}
	return totalSeconds, nil
}

// GetTopActivities gets top activities for a user
func (r *Repository) GetTopActivities(userID string, limit int) ([]ActivityHours, error) {
	rows, err := r.db.conn.Query(
		"SELECT activity_name, total_seconds FROM activity_hours WHERE user_id = $1 ORDER BY total_seconds DESC LIMIT $2",
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get top activities: %w", err)
	}
	defer rows.Close()

	var activities []ActivityHours
	for rows.Next() {
		var activity ActivityHours
		if err := rows.Scan(&activity.ActivityName, &activity.TotalSeconds); err != nil {
			log.Printf("Error scanning activity row: %v", err)
			continue
		}
		activity.UserID = userID
		activities = append(activities, activity)
	}

	return activities, nil
}

// GetVoiceChannelHours gets voice hours per channel for a user in a guild
func (r *Repository) GetVoiceChannelHours(userID, guildID string) ([]VoiceChannelHours, error) {
	rows, err := r.db.conn.Query(
		"SELECT channel_id, total_seconds FROM voice_channel_hours WHERE user_id = $1 AND guild_id = $2 ORDER BY total_seconds DESC",
		userID, guildID)
	if err != nil {
		return nil, fmt.Errorf("failed to get voice channel hours: %w", err)
	}
	defer rows.Close()

	var channelHours []VoiceChannelHours
	for rows.Next() {
		var ch VoiceChannelHours
		if err := rows.Scan(&ch.ChannelID, &ch.TotalSeconds); err != nil {
			log.Printf("Error scanning channel hours row: %v", err)
			continue
		}
		ch.UserID = userID
		ch.GuildID = guildID
		channelHours = append(channelHours, ch)
	}

	return channelHours, nil
}

// ActivityHours represents activity hours data
type ActivityHours struct {
	UserID       string
	ActivityName string
	TotalSeconds int64
}

// VoiceChannelHours represents voice channel hours data
type VoiceChannelHours struct {
	UserID       string
	GuildID      string
	ChannelID    string
	TotalSeconds int64
}
