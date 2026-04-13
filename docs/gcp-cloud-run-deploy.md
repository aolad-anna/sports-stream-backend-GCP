# GCP Cloud Run Deployment Guide

This project runs as a **single container** on Cloud Run. The container starts all internal services (`user`, `stream`, `notification`, `admin`, `analytics`) and exposes only the gateway on Cloud Run's `PORT`.

## 1) Required APIs

Enable these APIs in your GCP project:

- Cloud Run API
- Cloud Build API
- Artifact Registry API
- Secret Manager API

```bash
gcloud services enable \
  run.googleapis.com \
  cloudbuild.googleapis.com \
  artifactregistry.googleapis.com \
  secretmanager.googleapis.com
```

## 2) Create required secrets

Create these secrets once:

- `FIREBASE_CREDENTIALS` (raw Firebase service-account JSON content)
- `ADMIN_PANEL_PASSWORD`
- `ADMIN_PANEL_SESSION_SECRET`

Examples:

```bash
# FIREBASE_CREDENTIALS: upload JSON file contents as secret value
gcloud secrets create FIREBASE_CREDENTIALS --replication-policy=automatic || true
gcloud secrets versions add FIREBASE_CREDENTIALS --data-file=./firebase-adminsdk.json

gcloud secrets create ADMIN_PANEL_PASSWORD --replication-policy=automatic || true
printf "Admin@123" | gcloud secrets versions add ADMIN_PANEL_PASSWORD --data-file=-

gcloud secrets create ADMIN_PANEL_SESSION_SECRET --replication-policy=automatic || true
printf "change-me-with-32-or-more-characters" | gcloud secrets versions add ADMIN_PANEL_SESSION_SECRET --data-file=-
```

## 3) Grant Cloud Run service account access to secrets

By default Cloud Run uses the Compute Engine default service account unless you specify another.

```bash
PROJECT_ID="YOUR_PROJECT_ID"
PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
RUNTIME_SA="${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

for SECRET in FIREBASE_CREDENTIALS ADMIN_PANEL_PASSWORD ADMIN_PANEL_SESSION_SECRET; do
  gcloud secrets add-iam-policy-binding "$SECRET" \
    --member="serviceAccount:${RUNTIME_SA}" \
    --role="roles/secretmanager.secretAccessor"
done
```

## 4) Deploy with Cloud Build

Run from repository root:

```bash
gcloud builds submit --config cloudbuild.yaml \
  --substitutions=_REGION=europe-west1,_SERVICE_NAME=sports-stream-backend,_REPOSITORY=sports-stream,_ENV=production
```

Optional substitutions:

- `_ADMIN_PANEL_USERNAME`
- `_GCS_BUCKET`
- `_CDN_BASE_URL`
- `_TRANSCODER_LOCATION`
- `_VIEWER_SUB`
- `_STREAM_SUB`
- `_NOTIFICATION_SUB`

## 5) Verify deployment

```bash
SERVICE_URL="$(gcloud run services describe sports-stream-backend --region=europe-west1 --format='value(status.url)')"

curl -sS "$SERVICE_URL/health"
curl -i "$SERVICE_URL/admin/login"
```

Expected:

- `/health` returns gateway and all internal services as `up`
- `/admin/login` returns HTTP 200

## 6) Enable automatic deploy from GitHub (GCP Console)

Use Cloud Build Trigger so every push to `master` deploys automatically.

### A. Connect GitHub repository in GCP Console

1. Open **Cloud Build** in GCP Console.
2. Go to **Repositories**.
3. Click **Connect Repository**.
4. Choose **GitHub (Cloud Build GitHub App)**.
5. Authorize and select:
  - Owner: `aolad-anna`
  - Repository: `sports-stream-backend-staging`

### B. Create deploy trigger

1. In **Cloud Build** go to **Triggers**.
2. Click **Create Trigger**.
3. Configure:
  - Name: `deploy-master-cloud-run`
  - Event: `Push to a branch`
  - Source repository: `aolad-anna/sports-stream-backend-staging`
  - Branch (regex): `^master$`
  - Configuration: `Cloud Build configuration file (yaml/json)`
  - Location: `Repository`
  - File: `cloudbuild.yaml`

4. Add substitutions (optional, recommended):
  - `_REGION=europe-west1`
  - `_SERVICE_NAME=sports-stream-backend`
  - `_REPOSITORY=sports-stream`
  - `_ENV=production`

### C. Required IAM for build/deploy

Cloud Build runs as:

- `${PROJECT_NUMBER}@cloudbuild.gserviceaccount.com`

Grant this service account at least:

- `roles/run.admin`
- `roles/artifactregistry.writer`
- `roles/iam.serviceAccountUser` (for Cloud Run runtime service account)

Runtime service account still needs secret access from step 3 above:

- `roles/secretmanager.secretAccessor` on
  - `FIREBASE_CREDENTIALS`
  - `ADMIN_PANEL_PASSWORD`
  - `ADMIN_PANEL_SESSION_SECRET`

### D. Test trigger once

1. Open the trigger in **Cloud Build > Triggers**.
2. Click **Run**.
3. Confirm Cloud Run service URL responds:

```bash
SERVICE_URL="$(gcloud run services describe sports-stream-backend --region=europe-west1 --format='value(status.url)')"
curl -sS "$SERVICE_URL/health"
```

After this, each push to `master` on GitHub will auto-deploy.
