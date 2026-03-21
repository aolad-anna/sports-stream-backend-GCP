package util

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// init automatically loads .env file when any service starts.
// This means you never need to set env vars in GoLand manually.
// It looks for .env in the current directory and parent directories.
func init() {
	loadDotEnv()
}

// loadDotEnv searches for a .env file starting from the current directory
// and walking up to parent directories (max 5 levels).
func loadDotEnv() {
	dir, _ := os.Getwd()

	for i := 0; i < 5; i++ {
		path := dir + "/.env"
		if _, err := os.Stat(path); err == nil {
			if err := readEnvFile(path); err == nil {
				log.Printf(`{"service":"env","level":"info","msg":".env loaded","path":%q}`, path)
				return
			}
		}
		// Go up one directory
		parent := dir[:strings.LastIndex(dir, string(os.PathSeparator))]
		if parent == dir {
			break
		}
		dir = parent
	}
}

// readEnvFile reads a .env file and sets environment variables.
// Skips comments (#) and empty lines.
// Does NOT overwrite variables already set in the environment.
// This means GoLand environment field values take priority over .env file.
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

		// Only set if not already set — GoLand env vars take priority
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
