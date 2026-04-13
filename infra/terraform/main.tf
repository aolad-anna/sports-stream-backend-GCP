terraform {
  required_version = ">= 1.5.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

resource "google_pubsub_topic" "stream_events" {
  name = "stream-events"
}

resource "google_pubsub_topic" "viewer_events" {
  name = "viewer-events"
}

resource "google_pubsub_topic" "notification_events" {
  name = "notification-events"
}

resource "google_pubsub_subscription" "analytics_stream_events" {
  name  = "stream-events-analytics-sub"
  topic = google_pubsub_topic.stream_events.id
}

resource "google_pubsub_subscription" "analytics_viewer_events" {
  name  = "viewer-events-analytics-sub"
  topic = google_pubsub_topic.viewer_events.id
}

resource "google_pubsub_subscription" "notification_stream_events" {
  name  = "stream-events-notification-sub"
  topic = google_pubsub_topic.stream_events.id
}
