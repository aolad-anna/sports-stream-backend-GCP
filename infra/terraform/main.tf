terraform {
  required_version = ">= 1.5.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}

# ── Enable APIs ───────────────────────────────────────────────────────────────

resource "google_project_service" "apis" {
  for_each = toset([
    "run.googleapis.com",
    "pubsub.googleapis.com",
    "firestore.googleapis.com",
    "firebase.googleapis.com",
    "storage.googleapis.com",
    "cloudbuild.googleapis.com",
    "artifactregistry.googleapis.com",
    "livestream.googleapis.com",
    "transcoder.googleapis.com",
    "secretmanager.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

# ── Pub/Sub Topics ────────────────────────────────────────────────────────────

resource "google_pubsub_topic" "stream_events" {
  name = "stream-events"
  depends_on = [google_project_service.apis]
}

resource "google_pubsub_topic" "viewer_events" {
  name = "viewer-events"
  depends_on = [google_project_service.apis]
}

resource "google_pubsub_topic" "notification_events" {
  name = "notification-events"
  depends_on = [google_project_service.apis]
}

# ── Pub/Sub Subscriptions ─────────────────────────────────────────────────────

resource "google_pubsub_subscription" "analytics_stream_events" {
  name  = "stream-events-analytics-sub"
  topic = google_pubsub_topic.stream_events.id

  ack_deadline_seconds       = 60
  message_retention_duration = "86400s"

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "300s"
  }
}

resource "google_pubsub_subscription" "analytics_viewer_events" {
  name  = "viewer-events-analytics-sub"
  topic = google_pubsub_topic.viewer_events.id

  ack_deadline_seconds       = 60
  message_retention_duration = "86400s"

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "300s"
  }
}

resource "google_pubsub_subscription" "notification_stream_events" {
  name  = "stream-events-notification-sub"
  topic = google_pubsub_topic.stream_events.id

  ack_deadline_seconds       = 60
  message_retention_duration = "86400s"
}

# # ── GCS Bucket (videos, HLS segments, uploads) ────────────────────────────────
#
# resource "google_storage_bucket" "media" {
#   name                        = var.gcs_bucket_name
#   location                    = var.gcs_location
#   force_destroy               = false
#   uniform_bucket_level_access = true
#
#   cors {
#     origin          = ["*"]
#     method          = ["GET", "HEAD"]
#     response_header = ["Content-Type"]
#     max_age_seconds = 3600
#   }
#
#   lifecycle_rule {
#     condition {
#       age                = 7
#       matches_prefix     = ["uploads/"]
#     }
#     action {
#       type = "Delete"
#     }
#   }
#
#   depends_on = [google_project_service.apis]
# }

# Make HLS and video content publicly readable
# resource "google_storage_bucket_iam_member" "public_read" {
#   bucket = google_storage_bucket.media.name
#   role   = "roles/storage.objectViewer"
#   member = "allUsers"
# }

# ── Artifact Registry (Docker images) ────────────────────────────────────────

resource "google_artifact_registry_repository" "backend" {
  location      = var.region
  repository_id = "sports-stream-backend"
  format        = "DOCKER"
  description   = "Sports Stream backend Docker images"
  depends_on    = [google_project_service.apis]
}

# ── Cloud Run Service ─────────────────────────────────────────────────────────

resource "google_cloud_run_v2_service" "backend" {
  name     = var.cloud_run_service_name
  location = var.region

  template {
    scaling {
      min_instance_count = 0
      max_instance_count = 20
    }

    containers {
      image = "${var.region}-docker.pkg.dev/${var.project_id}/sports-stream-backend/backend:latest"

      resources {
        limits = {
          cpu    = "2"
          memory = "1Gi"
        }
        cpu_idle          = true
        startup_cpu_boost = true
      }

      env {
        name  = "GCP_PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "GCS_BUCKET"
        value = var.gcs_bucket_name
      }
      env {
        name  = "CDN_BASE_URL"
        value = "https://storage.googleapis.com/${var.gcs_bucket_name}"
      }
      env {
        name  = "TRANSCODER_LOCATION"
        value = "europe-west1"
      }
      env {
        name  = "LIVESTREAM_LOCATION"
        value = "us-central1"
      }
      env {
        name  = "RTDB_URL"
        value = var.rtdb_url
      }
      env {
        name  = "VIEWER_SUB"
        value = google_pubsub_subscription.analytics_viewer_events.name
      }
      env {
        name  = "STREAM_SUB"
        value = google_pubsub_subscription.analytics_stream_events.name
      }
      env {
        name  = "NOTIFICATION_SUB"
        value = google_pubsub_subscription.notification_stream_events.name
      }
      env {
        name = "RTDB_SECRET"
        value_source {
          secret_key_ref {
            secret  = "RTDB_SECRET"
            version = "latest"
          }
        }
      }
      env {
        name = "FIREBASE_CREDENTIALS"
        value_source {
          secret_key_ref {
            secret  = "FIREBASE_CREDENTIALS"
            version = "latest"
          }
        }
      }

      ports {
        container_port = 8080
      }

      startup_probe {
        http_get {
          path = "/health"
        }
        initial_delay_seconds = 5
        period_seconds        = 10
        failure_threshold     = 3
      }

      liveness_probe {
        http_get {
          path = "/health"
        }
        period_seconds    = 30
        failure_threshold = 3
      }
    }

    service_account = "${data.google_project.project.number}-compute@developer.gserviceaccount.com"
  }

  depends_on = [google_project_service.apis]
}

# Make Cloud Run service publicly accessible
resource "google_cloud_run_v2_service_iam_member" "public_invoker" {
  name     = google_cloud_run_v2_service.backend.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# ── IAM — Service account permissions ────────────────────────────────────────

data "google_project" "project" {
  project_id = var.project_id
}

locals {
  compute_sa = "${data.google_project.project.number}-compute@developer.gserviceaccount.com"
}

resource "google_project_iam_member" "compute_sa_roles" {
  for_each = toset([
    "roles/firebasedatabase.admin",
    # "roles/firestore.admin",
    "roles/pubsub.publisher",
    "roles/pubsub.subscriber",
    "roles/storage.admin",
    "roles/transcoder.admin",
    "roles/livestream.editor",
    "roles/secretmanager.secretAccessor",
    "roles/logging.logWriter",
  ])
  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${local.compute_sa}"
}

# ── Firestore Database ────────────────────────────────────────────────────────

resource "google_firestore_database" "default" {
  provider    = google-beta
  project     = var.project_id
  name        = "(default)"
  location_id = var.firestore_location
  type        = "FIRESTORE_NATIVE"

  depends_on = [google_project_service.apis]
}

# ── Secret Manager ────────────────────────────────────────────────────────────

resource "google_secret_manager_secret" "rtdb_secret" {
  secret_id = "RTDB_SECRET"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret" "firebase_credentials" {
  secret_id = "FIREBASE_CREDENTIALS"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

# ── Cloud Monitoring — Uptime Checks ─────────────────────────────────────────

resource "google_monitoring_uptime_check_config" "backend_health" {
  display_name = "Sports Stream Backend Health"
  timeout      = "10s"
  period       = "60s"

  http_check {
    path         = "/health"
    port         = 443
    use_ssl      = true
    validate_ssl = true
  }

  monitored_resource {
    type = "uptime_url"
    labels = {
      project_id = var.project_id
      host       = replace(google_cloud_run_v2_service.backend.uri, "https://", "")
    }
  }

  depends_on = [google_cloud_run_v2_service.backend]
}

# ── Cloud Monitoring — Alert Policies ────────────────────────────────────────

resource "google_monitoring_notification_channel" "email" {
  display_name = "Sports Stream Alerts"
  type         = "email"
  labels = {
    email_address = var.alert_email
  }
}

resource "google_monitoring_alert_policy" "backend_down" {
  display_name = "Backend Service Down"
  combiner     = "OR"

  conditions {
    display_name = "Uptime check failed"
    condition_threshold {
      filter          = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\""
      duration        = "60s"
      comparison      = "COMPARISON_LT"
      threshold_value = 1

      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_NEXT_OLDER"
        cross_series_reducer = "REDUCE_COUNT_TRUE"
        group_by_fields    = ["resource.label.host"]
      }
    }
  }

  notification_channels = [google_monitoring_notification_channel.email.id]

  alert_strategy {
    auto_close = "1800s"
  }
}

resource "google_monitoring_alert_policy" "high_latency" {
  display_name = "High API Latency"
  combiner     = "OR"

  conditions {
    display_name = "Request latency > 2s"
    condition_threshold {
      filter          = "metric.type=\"run.googleapis.com/request_latencies\" AND resource.type=\"cloud_run_revision\""
      duration        = "120s"
      comparison      = "COMPARISON_GT"
      threshold_value = 2000

      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_PERCENTILE_99"
      }
    }
  }

  notification_channels = [google_monitoring_notification_channel.email.id]
}

resource "google_monitoring_alert_policy" "high_error_rate" {
  display_name = "High Error Rate"
  combiner     = "OR"

  conditions {
    display_name = "5xx error rate > 5%"
    condition_threshold {
      filter          = "metric.type=\"run.googleapis.com/request_count\" AND resource.type=\"cloud_run_revision\" AND metric.label.response_code_class=\"5xx\""
      duration        = "120s"
      comparison      = "COMPARISON_GT"
      threshold_value = 5

      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_RATE"
      }
    }
  }

  notification_channels = [google_monitoring_notification_channel.email.id]
}

# ── Cloud Armor (DDoS protection) ─────────────────────────────────────────────

resource "google_compute_security_policy" "backend" {
  provider    = google-beta
  name        = "sports-stream-security-policy"
  description = "Cloud Armor DDoS protection for Sports Stream backend"

  # Block known bad IPs
  rule {
    action   = "deny(403)"
    priority = 1000
    match {
      expr {
        expression = "evaluatePreconfiguredExpr('xss-stable')"
      }
    }
    description = "Block XSS attacks"
  }

  rule {
    action   = "deny(403)"
    priority = 1001
    match {
      expr {
        expression = "evaluatePreconfiguredExpr('sqli-stable')"
      }
    }
    description = "Block SQL injection"
  }

  # Rate limiting — max 100 req/min per IP
  rule {
    action   = "throttle"
    priority = 2000
    match {
      versioned_expr = "SRC_IPS_V1"
      config {
        src_ip_ranges = ["*"]
      }
    }
    rate_limit_options {
      conform_action = "allow"
      exceed_action  = "deny(429)"
      rate_limit_threshold {
        count        = 100
        interval_sec = 60
      }
    }
    description = "Rate limit: 100 req/min per IP"
  }

  # Default allow
  rule {
    action   = "allow"
    priority = 2147483647
    match {
      versioned_expr = "SRC_IPS_V1"
      config {
        src_ip_ranges = ["*"]
      }
    }
    description = "Default allow"
  }

  depends_on = [google_project_service.apis]
}

# ── Cloud CDN + Load Balancer for GCS ────────────────────────────────────────

# resource "google_compute_backend_bucket" "media_cdn" {
#   name        = "sports-stream-media-cdn"
#   bucket_name = google_storage_bucket.media.name
#   enable_cdn  = true

#   cdn_policy {
#     cache_mode        = "CACHE_ALL_STATIC"
#     default_ttl       = 3600
#     max_ttl           = 86400
#     client_ttl        = 3600
#     negative_caching  = true
#   }
# }

# resource "google_compute_url_map" "media" {
#   name            = "sports-stream-media-lb"
#   default_service = google_compute_backend_bucket.media_cdn.id
# }

# resource "google_compute_target_https_proxy" "media" {
#   name             = "sports-stream-media-proxy"
#   url_map          = google_compute_url_map.media.id
#   ssl_certificates = [google_compute_managed_ssl_certificate.media.id]
# }

# resource "google_compute_managed_ssl_certificate" "media" {
#   provider = google-beta
#   name     = "sports-stream-media-cert"
#   managed {
#     domains = [var.cdn_domain]
#   }
# }
#
# resource "google_compute_global_forwarding_rule" "media" {
#   name       = "sports-stream-media-lb"
#   target     = google_compute_target_https_proxy.media.id
#   port_range = "443"
#   ip_address = google_compute_global_address.media.address
# }
#
# resource "google_compute_global_address" "media" {
#   name = "sports-stream-media-ip"
# }
