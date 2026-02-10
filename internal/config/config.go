package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Config struct {
	DataDir          string
	Addr             string
	RequestWindow    time.Duration
	RequestLimit     int
	ThrottleWindow   time.Duration
	ThrottleLimit    int
	BanDuration      time.Duration
	MessageWindow    time.Duration
	MessageLimit     int
	InMemoryLogLimit int
	AdminToken       string
	UIEnabled        bool
}

func DefaultDataDir() string {
	// OS-specific default
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "tower")
	}
	// Fallbacks
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, "tower")
		}
	}
	return filepath.Join(".", "data")
}

func DefaultConfig() Config {
	return Config{
		DataDir:          DefaultDataDir(),
		Addr:             ":8080",
		RequestWindow:    60 * time.Second,
		RequestLimit:     120,
		ThrottleWindow:   24 * time.Hour,
		ThrottleLimit:    5,
		BanDuration:      24 * time.Hour,
		MessageWindow:    60 * time.Second,
		MessageLimit:     10,
		InMemoryLogLimit: 5000,
		UIEnabled:        true,
	}
}

func NewToken(nbytes int) (string, error) {
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
