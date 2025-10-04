package utils

import (
	"fmt"
	"strings"
)

// FormatUserMention formats a user ID as a Discord mention
func FormatUserMention(userID string) string {
	return fmt.Sprintf("<@%s>", userID)
}

// ExtractUserIDFromMention extracts user ID from Discord mention
func ExtractUserIDFromMention(mention string) string {
	// Remove <@ and >
	userID := strings.TrimPrefix(mention, "<@")
	userID = strings.TrimSuffix(userID, ">")
	// Remove ! if present (for nickname mentions)
	userID = strings.TrimPrefix(userID, "!")
	return userID
}

// IsUserMention checks if a string is a valid user mention
func IsUserMention(text string) bool {
	return strings.HasPrefix(text, "<@") && strings.HasSuffix(text, ">")
}

// FormatLeaderboardEntry formats a leaderboard entry with rank, user, and duration
func FormatLeaderboardEntry(rank int, userMention, duration string) string {
	medal := ""
	switch rank {
	case 1:
		medal = "🥇"
	case 2:
		medal = "🥈"
	case 3:
		medal = "🥉"
	default:
		medal = fmt.Sprintf("%d.", rank)
	}
	
	return fmt.Sprintf("%s %s - %s", medal, userMention, duration)
}

// FormatChannelMention formats a channel ID as a Discord channel mention
func FormatChannelMention(channelID string) string {
	return fmt.Sprintf("<#%s>", channelID)
}

// TruncateString truncates a string to max length and adds ellipsis if needed
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
