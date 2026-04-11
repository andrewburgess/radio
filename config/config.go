package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port                string
	DBPath              string
	LibrespotBin        string
	LibrespotDeviceName string
	LibrespotDeviceType string
	LibrespotCacheDir   string
	BucketCount         int
	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyRedirectURI  string

	// Static audio
	StaticAudioFiles []string

	// Hardware
	DialI2CBus       string
	DialI2CAddr      string
	DialMinAngle     float64
	DialMaxAngle     float64
	ToggleGPIOPin    string
	PowerGPIOPin     string
	VolumeSPIDev     string
	VolumeSPIChannel int
	AlsaMixerControl string
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
		LibrespotDeviceType: getEnv("LIBRESPOT_DEVICE_TYPE", "speaker"),
		LibrespotCacheDir:   getEnv("LIBRESPOT_CACHE_DIR", "librespot-cache"),
	}

	var err error

	cfg.BucketCount, err = getEnvInt("BUCKET_COUNT", 12)
	if err != nil {
		return nil, fmt.Errorf("config: BUCKET_COUNT: %w", err)
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

	// Static audio — comma-separated list of MP3 file paths.
	cfg.StaticAudioFiles = getEnvStringSlice("STATIC_AUDIO_FILES", []string{"static/noise.mp3"})

	// Hardware
	cfg.DialI2CBus      = getEnv("DIAL_I2C_BUS", "I2C1")
	cfg.DialI2CAddr     = getEnv("DIAL_I2C_ADDR", "0x22")
	cfg.ToggleGPIOPin   = getEnv("TOGGLE_GPIO_PIN", "GPIO17")
	cfg.PowerGPIOPin    = getEnv("POWER_GPIO_PIN", "GPIO27")
	cfg.VolumeSPIDev    = getEnv("VOLUME_SPI_DEV", "SPI0.0")
	cfg.AlsaMixerControl = getEnv("ALSA_MIXER_CONTROL", "Master")

	cfg.DialMinAngle, err = getEnvFloat("DIAL_MIN_ANGLE", 0)
	if err != nil {
		return nil, fmt.Errorf("config: DIAL_MIN_ANGLE: %w", err)
	}
	cfg.DialMaxAngle, err = getEnvFloat("DIAL_MAX_ANGLE", 270)
	if err != nil {
		return nil, fmt.Errorf("config: DIAL_MAX_ANGLE: %w", err)
	}
	cfg.VolumeSPIChannel, err = getEnvInt("VOLUME_SPI_CHANNEL", 0)
	if err != nil {
		return nil, fmt.Errorf("config: VOLUME_SPI_CHANNEL: %w", err)
	}

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

func getEnvStringSlice(key string, defaultVal []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return defaultVal
	}
	return out
}

func getEnvFloat(key string, defaultVal float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float %q", v)
	}
	return f, nil
}
