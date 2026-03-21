package util

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// init automatically loads .env file when any service starts.
// In Docker/Fly.io there is no .env file — env vars are injected directly.
// In local GoLand the .env file is loaded from the project root.
func init() {
	loadDotEnv()
}

// loadDotEnv searches for a .env file starting from the current directory
// walking up to parent directories safely using filepath.Dir.
func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}

	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, ".env")
		if _, statErr := os.Stat(path); statErr == nil {
			if readErr := readEnvFile(path); readErr == nil {
				log.Printf(`{"service":"env","level":"info","msg":".env loaded","path":%q}`, path)
			}
			return
		}

		// Go up one directory safely
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root — stop searching
			break
		}
		dir = parent
	}
	// No .env found — perfectly fine in Docker/Fly.io/production
	// env vars are injected directly by the platform
}

// readEnvFile reads a .env file and sets environment variables.
// Skips comments (#) and empty lines.
// Does NOT overwrite variables already set — platform-injected vars take priority.
func readEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on first = only
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Only set if not already set — injected env vars take priority
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	return scanner.Err()
}

// MustGetenv returns the value of key or fatals if empty.
func MustGetenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf(`{"level":"fatal","msg":"missing required env var","key":%q}`, key)
	}
	return v
}

// Getenv returns the value of key, or defaultVal if not set.
func Getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
