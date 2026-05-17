variable "project_id" {
  description = "GCP project ID"
  type        = string
}

variable "region" {
  description = "Default GCP region for Cloud Run and Artifact Registry"
  type        = string
  default     = "europe-west2"
}

variable "gcs_location" {
  description = "GCS bucket location"
  type        = string
  default     = "EU"
}

variable "gcs_bucket_name" {
  description = "GCS bucket name for media storage"
  type        = string
}

variable "firestore_location" {
  description = "Firestore database location"
  type        = string
  default     = "eur3"
}

variable "cloud_run_service_name" {
  description = "Cloud Run service name"
  type        = string
  default     = "sports-stream-backend-staging"
}

variable "rtdb_url" {
  description = "Firebase Realtime Database URL"
  type        = string
  default     = "https://sports-stream-66553-default-rtdb.europe-west1.firebasedatabase.app"
}

variable "alert_email" {
  description = "Email address for monitoring alerts"
  type        = string
  default     = "aolad.anna@gmail.com"
}

variable "cdn_domain" {
  description = "Custom domain for CDN (optional)"
  type        = string
  default     = "media.sports-stream.app"
}
