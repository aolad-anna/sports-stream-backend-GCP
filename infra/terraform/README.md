# Sports Stream — Terraform Infrastructure (CCC'26)

Infrastructure as Code for the Sports Stream live sports streaming platform.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  GCP Project                        │
│                                                     │
│  Cloud Run ──► Firestore (streams, users)           │
│       │                                             │
│       ├──► GCS Bucket (HLS, uploads, videos)        │
│       │                                             │
│       ├──► Pub/Sub ──► Analytics Sub                │
│       │           └──► Notification Sub             │
│       │                                             │
│       └──► Firebase RTDB (live chat)                │
└─────────────────────────────────────────────────────┘
```

## Resources Managed

| Resource | Description |
|---|---|
| `google_cloud_run_v2_service` | Backend API (6 microservices in one container) |
| `google_storage_bucket` | Media storage — HLS segments, video uploads |
| `google_pubsub_topic` × 3 | stream-events, viewer-events, notification-events |
| `google_pubsub_subscription` × 3 | Analytics and notification subscribers |
| `google_artifact_registry_repository` | Docker image registry |
| `google_firestore_database` | Streams, users, analytics data |
| `google_project_iam_member` | IAM roles for compute service account |
| `google_project_service` | Enable required GCP APIs |

## Usage

### Prerequisites
- Terraform v1.5+
- `gcloud` CLI authenticated
- GCP project with billing enabled

### Steps

```bash
# 1. Authenticate
gcloud auth application-default login

# 2. Clone and navigate
cd infrastructure/terraform

# 3. Configure
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your project values

# 4. Initialize
terraform init

# 5. Plan (review changes)
terraform plan -out=tfplan

# 6. Apply
terraform apply tfplan
```

### Expected output
```
Apply complete! Resources: 18 added, 0 changed, 0 destroyed.

Outputs:
  cloud_run_url         = "https://sports-stream-backend-staging-xxx.run.app"
  gcs_bucket            = "sports-stream-66553.appspot.com"
  pubsub_topics         = ["stream-events", "viewer-events", "notification-events"]
  pubsub_subscriptions  = ["stream-events-analytics-sub", ...]
```

## CCC'26 Demo Evidence

Save these for jury submission:

```bash
# Save plan output
terraform plan 2>&1 | tee terraform-plan.txt

# Save apply output  
terraform apply -auto-approve 2>&1 | tee terraform-apply.txt

# Save state summary
terraform show 2>&1 | tee terraform-show.txt
```

Screenshots needed:
- GCP Console → Cloud Run → service running
- GCP Console → Pub/Sub → topics and subscriptions
- GCP Console → Cloud Storage → bucket with content
- Terraform apply terminal output
