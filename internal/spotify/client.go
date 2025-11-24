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

// GetSpotifyTracks mengembalikan daftar query ("Artist - Title") dari URL Spotify
func (c *Client) GetSpotifyTracks(url string) ([]string, error) {
	ctx := context.Background()

	if strings.Contains(url, "playlist/") {
		return c.getPlaylistTracks(ctx, url)
	}
	if strings.Contains(url, "album/") {
		return c.getAlbumTracks(ctx, url)
	}
	if strings.Contains(url, "track/") {
		query, err := c.getTrack(ctx, url)
		if err != nil {
			return nil, err
		}
		return []string{query}, nil
	}

	return nil, fmt.Errorf("link spotify tidak dikenali")
}

func (c *Client) getTrack(ctx context.Context, url string) (string, error) {
	id := extractID(url, "track/")
	if id == "" {
		return "", fmt.Errorf("invalid track id")
	}

	track, err := c.api.GetTrack(ctx, spotify.ID(id))
	if err != nil {
		return "", err
	}
	return formatTrack(track.SimpleTrack), nil
}

func (c *Client) getPlaylistTracks(ctx context.Context, url string) ([]string, error) {
	id := extractID(url, "playlist/")
	if id == "" {
		return nil, fmt.Errorf("invalid playlist id")
	}

	playlistItems, err := c.api.GetPlaylistItems(ctx, spotify.ID(id), spotify.Limit(50))
	if err != nil {
		return nil, err
	}

	var tracks []string
	for _, item := range playlistItems.Items {
		if item.Track.Track != nil {
			tracks = append(tracks, formatTrack(item.Track.Track.SimpleTrack))
		}
	}
	return tracks, nil
}

func (c *Client) getAlbumTracks(ctx context.Context, url string) ([]string, error) {
	id := extractID(url, "album/")
	if id == "" {
		return nil, fmt.Errorf("invalid album id")
	}

	album, err := c.api.GetAlbum(ctx, spotify.ID(id))
	if err != nil {
		return nil, err
	}

	var tracks []string
	for _, track := range album.Tracks.Tracks {
		tracks = append(tracks, formatTrack(track))
	}
	return tracks, nil
}

// Helper: Extract ID dari URL
func extractID(url, prefix string) string {
	re := regexp.MustCompile(prefix + `([a-zA-Z0-9]+)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// Helper: Format "Artist - Title"
func formatTrack(track spotify.SimpleTrack) string {
	var artists []string
	for _, a := range track.Artists {
		artists = append(artists, a.Name)
	}
	return fmt.Sprintf("%s - %s", strings.Join(artists, ", "), track.Name)
}
