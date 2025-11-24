package spotify

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/zmb3/spotify/v2"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"golang.org/x/oauth2/clientcredentials"
)

type Client struct {
	api *spotify.Client
}

func New(clientID, clientSecret string) (*Client, error) {
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("spotify credentials missing")
	}

	ctx := context.Background()
	config := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     spotifyauth.TokenURL,
	}
	httpClient := config.Client(ctx)

	client := spotify.New(httpClient)

	return &Client{api: client}, nil
}

// GetTrackInfo mengambil Artist - Title dari Spotify URL
func (c *Client) GetTrackInfo(url string) (string, error) {
	// Parsing ID dari URL (contoh: https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC)
	re := regexp.MustCompile(`track/([a-zA-Z0-9]+)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) < 2 {
		return "", fmt.Errorf("invalid spotify track url")
	}
	trackID := matches[1]

	track, err := c.api.GetTrack(context.Background(), spotify.ID(trackID))
	if err != nil {
		return "", err
	}

	// Gabungkan semua artis
	var artists []string
	for _, a := range track.Artists {
		artists = append(artists, a.Name)
	}
	artistStr := strings.Join(artists, ", ")

	// Return format: "Artist Name - Song Title"
	return fmt.Sprintf("%s - %s", artistStr, track.Name), nil
}