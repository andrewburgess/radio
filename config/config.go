package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                  string
	DBPath                string
	LibrespotBin          string
	LibrespotDeviceName   string
	LibrespotCacheDir     string
	BucketCount           int
	PodcastWindowDays     int
	PodcastCronInterval   time.Duration
	SpotifyClientID       string
	SpotifyClientSecret   string
	SpotifyRedirectURI    string
	SpotifyTokenFile      string
	SpotifyTestPlaylist   string
}

func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// strip optional surrounding quotes
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		// don't overwrite vars already set in the environment
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func Load() (*Config, error) {
	loadDotEnv()

	cfg := &Config{
		Port:                getEnv("PORT", "8080"),
		DBPath:              getEnv("DB_PATH", "radio.db"),
		LibrespotBin:        getEnv("LIBRESPOT_BIN", "librespot"),
		LibrespotDeviceName: getEnv("LIBRESPOT_DEVICE_NAME", "Zenith Radio"),
		LibrespotCacheDir:   getEnv("LIBRESPOT_CACHE_DIR", "librespot-cache"),
	}

	var err error

	cfg.BucketCount, err = getEnvInt("BUCKET_COUNT", 12)
	if err != nil {
		return nil, fmt.Errorf("config: BUCKET_COUNT: %w", err)
	}

	cfg.PodcastWindowDays, err = getEnvInt("PODCAST_WINDOW_DAYS", 14)
	if err != nil {
		return nil, fmt.Errorf("config: PODCAST_WINDOW_DAYS: %w", err)
	}

	cfg.PodcastCronInterval, err = getEnvDuration("PODCAST_CRON_INTERVAL", 6*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: PODCAST_CRON_INTERVAL: %w", err)
	}

	cfg.SpotifyClientID = os.Getenv("SPOTIFY_CLIENT_ID")
	if cfg.SpotifyClientID == "" {
		return nil, fmt.Errorf("config: SPOTIFY_CLIENT_ID is required")
	}

	cfg.SpotifyClientSecret = os.Getenv("SPOTIFY_CLIENT_SECRET")
	if cfg.SpotifyClientSecret == "" {
		return nil, fmt.Errorf("config: SPOTIFY_CLIENT_SECRET is required")
	}

	cfg.SpotifyRedirectURI = os.Getenv("SPOTIFY_REDIRECT_URI")
	if cfg.SpotifyRedirectURI == "" {
		return nil, fmt.Errorf("config: SPOTIFY_REDIRECT_URI is required")
	}

	cfg.SpotifyTokenFile    = getEnv("SPOTIFY_TOKEN_FILE", "spotify-tokens.json")
	cfg.SpotifyTestPlaylist = os.Getenv("SPOTIFY_TEST_PLAYLIST")

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", v)
	}
	return n, nil
}

func getEnvDuration(key string, defaultVal time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", v)
	}
	return d, nil
}
