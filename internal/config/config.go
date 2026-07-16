// Package config loads the relay's runtime settings from the environment, so the service
// stays a single self-contained binary that is easy to run under Docker.
package config

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration for the relay.
type Config struct {
	// HTTPAddr is the address the relay listens on, e.g. ":8090".
	HTTPAddr string
	// FCMCredentialsFile points to the Firebase service-account JSON the relay forwards
	// with. Required: the relay exists to hold this one credential.
	FCMCredentialsFile string
	// DBPath is the SQLite file that stores hashed registration keys.
	DBPath string
	// AdminToken guards the /admin endpoints (list and revoke keys). Empty disables them.
	AdminToken string
	// RegisterPerHour and RegisterBurst bound how often one IP may mint new keys.
	RegisterPerHour int
	RegisterBurst   int
	// SendPerMinute and SendBurst bound how often one key may fan out notifications.
	SendPerMinute int
	SendBurst     int
	// MaxMessages caps how many device tokens a single /v1/send request may carry.
	MaxMessages int
}

// Load reads configuration from the environment, applying defaults that suit a single
// maintainer-run relay.
func Load() Config {
	return Config{
		HTTPAddr:           getenv("RELAY_HTTP_ADDR", ":8090"),
		FCMCredentialsFile: getenv("RELAY_FCM_CREDENTIALS_FILE", ""),
		DBPath:             getenv("RELAY_DB_PATH", "/data/relay.db"),
		AdminToken:         getenv("RELAY_ADMIN_TOKEN", ""),
		RegisterPerHour:    getint("RELAY_REGISTER_PER_HOUR", 5),
		RegisterBurst:      getint("RELAY_REGISTER_BURST", 3),
		SendPerMinute:      getint("RELAY_SEND_PER_MINUTE", 120),
		SendBurst:          getint("RELAY_SEND_BURST", 60),
		MaxMessages:        getint("RELAY_MAX_MESSAGES", 500),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getint(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
