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

// AddDailyStats adds daily statistics
func (r *Repository) AddDailyStats(date, userID, guildID string, voiceSeconds, activitySeconds int64, activityName string) error {
	_, err := r.db.conn.Exec(`
		INSERT INTO daily_stats (date, user_id, guild_id, voice_seconds, activity_seconds, activity_name)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (date, user_id, guild_id, activity_name) 
		DO UPDATE SET 
			voice_seconds = daily_stats.voice_seconds + EXCLUDED.voice_seconds,
			activity_seconds = daily_stats.activity_seconds + EXCLUDED.activity_seconds`,
		date, userID, guildID, voiceSeconds, activitySeconds, activityName)
	if err != nil {
		return fmt.Errorf("failed to add daily stats: %w", err)
	}
	return nil
}

// AddWeeklyStats adds weekly statistics
func (r *Repository) AddWeeklyStats(weekStart, userID, guildID string, voiceSeconds, activitySeconds int64, activityName string) error {
	_, err := r.db.conn.Exec(`
		INSERT INTO weekly_stats (week_start, user_id, guild_id, voice_seconds, activity_seconds, activity_name)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (week_start, user_id, guild_id, activity_name) 
		DO UPDATE SET 
			voice_seconds = weekly_stats.voice_seconds + EXCLUDED.voice_seconds,
			activity_seconds = weekly_stats.activity_seconds + EXCLUDED.activity_seconds`,
		weekStart, userID, guildID, voiceSeconds, activitySeconds, activityName)
	if err != nil {
		return fmt.Errorf("failed to add weekly stats: %w", err)
	}
	return nil
}

// GetVoiceLeaderboard gets voice leaderboard for a guild
func (r *Repository) GetVoiceLeaderboard(guildID string, limit int) ([]LeaderboardEntry, error) {
	rows, err := r.db.conn.Query(`
		SELECT user_id, total_seconds 
		FROM voice_hours 
		WHERE guild_id = $1 
		ORDER BY total_seconds DESC 
		LIMIT $2`,
		guildID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get voice leaderboard: %w", err)
	}
	defer rows.Close()

	var entries []LeaderboardEntry
	rank := 1
	for rows.Next() {
		var entry LeaderboardEntry
		if err := rows.Scan(&entry.UserID, &entry.TotalSeconds); err != nil {
			log.Printf("Error scanning leaderboard row: %v", err)
			continue
		}
		entry.Rank = rank
		entries = append(entries, entry)
		rank++
	}

	return entries, nil
}

// GetActivityLeaderboard gets activity leaderboard for a specific activity
func (r *Repository) GetActivityLeaderboard(activityName string, limit int) ([]LeaderboardEntry, error) {
	rows, err := r.db.conn.Query(`
		SELECT user_id, total_seconds 
		FROM activity_hours 
		WHERE activity_name = $1 
		ORDER BY total_seconds DESC 
		LIMIT $2`,
		activityName, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get activity leaderboard: %w", err)
	}
	defer rows.Close()

	var entries []LeaderboardEntry
	rank := 1
	for rows.Next() {
		var entry LeaderboardEntry
		if err := rows.Scan(&entry.UserID, &entry.TotalSeconds); err != nil {
			log.Printf("Error scanning activity leaderboard row: %v", err)
			continue
		}
		entry.Rank = rank
		entries = append(entries, entry)
		rank++
	}

	return entries, nil
}

// GetUserComparison gets comparison data for two users
func (r *Repository) GetUserComparison(userID1, userID2, guildID string) ([]UserComparison, error) {
	var comparisons []UserComparison
	
	// Get data for both users
	userIDs := []string{userID1, userID2}
	for _, userID := range userIDs {
		comparison := UserComparison{UserID: userID}
		
		// Get voice hours for this guild
		voiceSeconds, err := r.GetVoiceHours(userID, guildID)
		if err != nil {
			log.Printf("Error getting voice hours for user %s: %v", userID, err)
		}
		comparison.VoiceSeconds = voiceSeconds
		
		// Get top activities
		activities, err := r.GetTopActivities(userID, 3)
		if err != nil {
			log.Printf("Error getting top activities for user %s: %v", userID, err)
		}
		comparison.TopActivities = activities
		
		// Get channel hours
		channelHours, err := r.GetVoiceChannelHours(userID, guildID)
		if err != nil {
			log.Printf("Error getting channel hours for user %s: %v", userID, err)
		}
		comparison.ChannelHours = channelHours
		
		comparisons = append(comparisons, comparison)
	}
	
	return comparisons, nil
}

// GetWeeklyReport gets weekly report for a user
func (r *Repository) GetWeeklyReport(userID, guildID string, weekStart string) ([]WeeklyStats, error) {
	rows, err := r.db.conn.Query(`
		SELECT week_start, user_id, guild_id, voice_seconds, activity_seconds, activity_name
		FROM weekly_stats 
		WHERE user_id = $1 AND guild_id = $2 AND week_start = $3
		ORDER BY voice_seconds DESC, activity_seconds DESC`,
		userID, guildID, weekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to get weekly report: %w", err)
	}
	defer rows.Close()

	var stats []WeeklyStats
	for rows.Next() {
		var stat WeeklyStats
		if err := rows.Scan(&stat.WeekStart, &stat.UserID, &stat.GuildID, 
			&stat.VoiceSeconds, &stat.ActivitySeconds, &stat.ActivityName); err != nil {
			log.Printf("Error scanning weekly stats row: %v", err)
			continue
		}
		stats = append(stats, stat)
	}

	return stats, nil
}

// GetMonthlyReport gets monthly report for a user (last 4 weeks)
func (r *Repository) GetMonthlyReport(userID, guildID string) ([]WeeklyStats, error) {
	rows, err := r.db.conn.Query(`
		SELECT week_start, user_id, guild_id, voice_seconds, activity_seconds, activity_name
		FROM weekly_stats 
		WHERE user_id = $1 AND guild_id = $2 
		AND week_start >= CURRENT_DATE - INTERVAL '28 days'
		ORDER BY week_start DESC`,
		userID, guildID)
	if err != nil {
		return nil, fmt.Errorf("failed to get monthly report: %w", err)
	}
	defer rows.Close()

	var stats []WeeklyStats
	for rows.Next() {
		var stat WeeklyStats
		if err := rows.Scan(&stat.WeekStart, &stat.UserID, &stat.GuildID, 
			&stat.VoiceSeconds, &stat.ActivitySeconds, &stat.ActivityName); err != nil {
			log.Printf("Error scanning monthly stats row: %v", err)
			continue
		}
		stats = append(stats, stat)
	}

	return stats, nil
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

// DailyStats represents daily statistics data
type DailyStats struct {
	Date            string
	UserID          string
	GuildID         string
	VoiceSeconds    int64
	ActivitySeconds int64
	ActivityName    string
}

// WeeklyStats represents weekly statistics data
type WeeklyStats struct {
	WeekStart       string
	UserID          string
	GuildID         string
	VoiceSeconds    int64
	ActivitySeconds int64
	ActivityName    string
}

// LeaderboardEntry represents a leaderboard entry
type LeaderboardEntry struct {
	UserID       string
	Username     string
	TotalSeconds int64
	Rank         int
}

// UserComparison represents user comparison data
type UserComparison struct {
	UserID         string
	Username       string
	VoiceSeconds   int64
	TopActivities  []ActivityHours
	ChannelHours   []VoiceChannelHours
}
