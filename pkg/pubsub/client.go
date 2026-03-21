package pubsub

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"cloud.google.com/go/pubsub"
	"google.golang.org/api/option"
)

var client *pubsub.Client

// InitClient creates the shared Pub/Sub client.
// credsFile is the path to the service-account JSON — same file used by Firebase.
func InitClient(ctx context.Context, projectID string, credsFile string) (*pubsub.Client, error) {
	var opts []option.ClientOption
	if credsFile != "" {
		if strings.HasPrefix(strings.TrimSpace(credsFile), "{") {
			opts = append(opts, option.WithCredentialsJSON([]byte(credsFile)))
		} else {
			opts = append(opts, option.WithCredentialsFile(credsFile))
		}
	}
	var err error
	client, err = pubsub.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, err
	}
	log.Println(`{"service":"pubsub","level":"info","msg":"pubsub client initialised"}`)
	return client, nil
}

// GetClient returns the initialised Pub/Sub client. Panics if InitClient was not called.
func GetClient() *pubsub.Client {
	if client == nil {
		panic("pubsub.InitClient must be called before GetClient")
	}
	return client
}

// PublishEvent marshals data to JSON and publishes to topicID.
// Blocks until the server acknowledges. Call in a goroutine for non-blocking use.
func PublishEvent(ctx context.Context, topicID string, data map[string]any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	topic := GetClient().Topic(topicID)
	result := topic.Publish(ctx, &pubsub.Message{Data: payload})
	_, err = result.Get(ctx)
	if err != nil {
		log.Printf(`{"service":"pubsub","level":"warn","msg":"publish failed","topic":%q,"error":%q}`, topicID, err.Error())
	}
	return err
}

// Subscribe creates a blocking subscription receiver on subID.
// handler returns true to ack, false to nack (triggers Pub/Sub retry).
// Call in a goroutine — this blocks until ctx is cancelled.
func Subscribe(ctx context.Context, subID string, handler func(data []byte) bool) error {
	sub := GetClient().Subscription(subID)
	return sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
		if handler(msg.Data) {
			msg.Ack()
		} else {
			msg.Nack()
		}
	})
}
