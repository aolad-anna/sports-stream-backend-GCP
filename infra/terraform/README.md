# Terraform Baseline

This directory contains a baseline Infrastructure as Code setup for CCC'26 evidence.

## Included Resources

- Pub/Sub topics:
  - stream-events
  - viewer-events
  - notification-events
- Pub/Sub subscriptions:
  - stream-events-analytics-sub
  - viewer-events-analytics-sub
  - stream-events-notification-sub

## Usage

1. Install Terraform v1.5+.
2. Authenticate to GCP (for example using gcloud auth application-default login).
3. Copy terraform.tfvars.example to terraform.tfvars and set your project.
4. Run:

```bash
terraform init
terraform plan
terraform apply
```

## Demo Evidence Checklist

- Save terminal output of plan and apply.
- Save screenshot of created topics/subscriptions in GCP console.
- Attach outputs to final jury submission.
