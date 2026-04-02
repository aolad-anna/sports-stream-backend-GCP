output "pubsub_topics" {
  value = [
    google_pubsub_topic.stream_events.name,
    google_pubsub_topic.viewer_events.name,
    google_pubsub_topic.notification_events.name,
  ]
}

output "pubsub_subscriptions" {
  value = [
    google_pubsub_subscription.analytics_stream_events.name,
    google_pubsub_subscription.analytics_viewer_events.name,
    google_pubsub_subscription.notification_stream_events.name,
  ]
}
