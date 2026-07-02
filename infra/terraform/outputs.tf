output "pubsub_topics" {
  description = "Pub/Sub topic names"
  value = [
    google_pubsub_topic.stream_events.name,
    google_pubsub_topic.viewer_events.name,
    google_pubsub_topic.notification_events.name,
  ]
}

output "pubsub_subscriptions" {
  description = "Pub/Sub subscription names"
  value = [
    google_pubsub_subscription.analytics_stream_events.name,
    google_pubsub_subscription.analytics_viewer_events.name,
    google_pubsub_subscription.notification_stream_events.name,
  ]
}

output "cloud_run_url" {
  description = "Cloud Run service URL"
  value       = google_cloud_run_v2_service.backend.uri
}

# output "gcs_bucket" {
#   description = "GCS media bucket name"
#   value       = google_storage_bucket.media.name
# }

output "artifact_registry" {
  description = "Artifact Registry repository URL"
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/sports-stream-backend"
}

output "firestore_database" {
  description = "Firestore database name"
  value       = google_firestore_database.default.name
}

# output "cdn_ip" {
#   description = "Global CDN IP address for media"
#   value       = google_compute_global_address.media.address
# }

output "monitoring_uptime_check" {
  description = "Uptime check display name"
  value       = google_monitoring_uptime_check_config.backend_health.display_name
}

output "security_policy" {
  description = "Cloud Armor security policy name"
  value       = google_compute_security_policy.backend.name
}
