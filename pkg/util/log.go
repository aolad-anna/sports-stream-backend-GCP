package util

import (
	"encoding/json"
	"log"
	"time"
)

// LogJSON emits a structured log entry with a consistent envelope.
func LogJSON(service, level, message string, fields map[string]any) {
	entry := map[string]any{
		"service": service,
		"level":   level,
		"msg":     message,
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
	}

	for k, v := range fields {
		entry[k] = v
	}

	b, err := json.Marshal(entry)
	if err != nil {
		log.Printf("{\"service\":%q,\"level\":%q,\"msg\":%q,\"marshalError\":%q}", service, level, message, err.Error())
		return
	}

	log.Print(string(b))
}
