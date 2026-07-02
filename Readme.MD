# Sports Stream Backend

A Go microservices backend for a live sports streaming platform, built on Google Cloud Platform (Firestore, Pub/Sub, Cloud Storage, Firebase Auth/FCM) with a single API gateway as the public entry point.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Project Structure](#project-structure)
- [Services](#services)
- [Tech Stack](#tech-stack)
- [Getting Started](#getting-started)
    - [Prerequisites](#prerequisites)
    - [Local Development (Docker Compose)](#local-development-docker-compose)
    - [Running a Single Service](#running-a-single-service)
- [Configuration](#configuration)
- [Deployment](#deployment)
- [Infrastructure](#infrastructure)
- [Testing](#testing)
- [Admin & Public Assets](#admin--public-assets)

## Overview

The platform ingests, transcodes, and serves live/on-demand sports video, tracks viewer analytics in real time, and pushes notifications for stream events. The backend evolved from a single mobile-app API concept into a **gateway + multiple Go microservices** architecture, communicating over Pub/Sub and backed by Firestore, in order to separate concerns, improve reliability, and support bursty live-event traffic.

## Architecture

```
Client (Android / Web)
        │
        ▼
   API Gateway  (health checks, routing, single public entry point)
        │
   ┌────┼─────────┬─────────────┬───────────┬────────────┐
   ▼    ▼         ▼             ▼           ▼            ▼
 user  stream  analytics   notification   admin        video
service service service    service      service      service
   │      │        │             │           │            │
   └──────┴────────┴─────────────┴───────────┴────────────┘
                        │
        Firestore · Pub/Sub · Cloud Storage · Firebase Auth/FCM
```

- **Gateway** — single public entry point; routes requests to internal services and exposes an aggregated health/status page.
- **Services** communicate asynchronously via **Pub/Sub** topics/subscriptions (e.g. viewer events, stream events, notification events) and persist state in **Firestore**.
- **Video/media** is stored in **Cloud Storage** and served via CDN URLs; transcoding is handled with `ffmpeg`.
- In production, all services + gateway run in a **single container** (see [Deployment](#deployment)), with only the gateway port exposed.

Further architecture detail and diagrams: [`docs/architecture-evolution.md`](docs/architecture-evolution.md).

## Project Structure

```
.
├── gateway/              # API gateway (routing, health aggregation)
├── services/
│   ├── user/             # User accounts & auth
│   ├── stream/           # Live/VOD stream management
│   ├── analytics/        # Viewer & stream analytics (Pub/Sub consumer)
│   ├── notification/     # Push notifications (Pub/Sub consumer, no HTTP)
│   ├── admin/            # Admin panel backend
│   └── video/            # Video ingest/transcoding
├── cmd/
│   ├── transcode/         # CLI: video transcoding job
│   ├── migrate-analytics/ # CLI: analytics data migration
│   └── reset-viewers/     # CLI: reset viewer counters
├── pkg/
│   ├── firebase/          # Shared Firebase client
│   ├── middleware/        # Auth & security middleware
│   ├── pubsub/             # Shared Pub/Sub client
│   └── util/               # Logging & env helpers
├── public/                # Static assets (dashboard, upload UI, admin, health page)
├── k8s/                    # Kubernetes manifests (deployments, HPA)
├── infra/terraform/        # Terraform infrastructure-as-code
├── scripts/                 # Deployment scripts
├── docs/                     # Architecture, deployment & planning docs
├── Dockerfile                # Multi-stage build → single runtime container
├── docker-compose.yml        # Local multi-service orchestration
├── cloudbuild.yaml            # GCP Cloud Build pipeline
├── start.sh / Procfile        # Container/process entry point
└── firebase.json / firestore.rules
```

## Services

| Service | Port | Role | Notes |
|---|---|---|---|
| `gateway` | 8080 | Public entry point, request routing, health aggregation | Only externally exposed port in production |
| `user-service` | 8081 | User accounts, authentication | |
| `stream-service` | 8082 | Stream lifecycle, CDN-backed media URLs | Uses GCS bucket + CDN base URL |
| `admin-service` | 8084 | Admin panel backend | Session-based auth |
| `analytics-service` | 8085 (+9090 metrics) | Viewer/stream analytics | Pub/Sub consumer, exposes Prometheus metrics |
| `notification-service` | — | Push notifications | Pub/Sub consumer only, no HTTP port |
| `video-service` | — | Video ingest/transcoding | Built and run as its own binary |

## Tech Stack

- **Language:** Go 1.25+
- **Cloud:** Google Cloud Platform — Firestore, Pub/Sub, Cloud Storage, Cloud Run
- **Auth/Push:** Firebase Auth, Firebase Cloud Messaging (FCM)
- **Hosting (static/admin):** Firebase Hosting
- **Media:** ffmpeg (transcoding)
- **Containers/Orchestration:** Docker, Docker Compose, Kubernetes (manifests in `k8s/`)
- **IaC:** Terraform (`infra/terraform/`)
- **CI/CD:** Google Cloud Build (`cloudbuild.yaml`), GitHub Actions (`.github/workflows/`)
- **Load testing:** k6 (`k6-load-test.js`)

## Getting Started

### Prerequisites

- Go 1.25+
- Docker & Docker Compose
- A GCP project with Firestore, Pub/Sub, and Cloud Storage enabled
- A Firebase service account (credentials JSON) for the project

### Local Development (Docker Compose)

1. Copy the environment template and fill in your values:
   ```bash
   cp .env.example .env
   # set GCP_PROJECT_ID and FIREBASE_CREDENTIALS at minimum
   ```
2. Build and start all services:
   ```bash
   docker-compose up --build
   ```
3. Stop everything:
   ```bash
   docker-compose down
   ```

### Running a Single Service

```bash
docker-compose up user-service
```

Or run a service directly with Go:

```bash
go run ./services/user
```

### Running Everything in One Container (production-style)

```bash
docker build -t sports-stream-backend .
docker run -p 8080:8080 --env-file .env sports-stream-backend
```

This builds all service binaries plus the gateway into a single Alpine image and starts them via `start.sh`, exposing only the gateway on port 8080.

## Configuration

Key environment variables (see `docker-compose.yml` for the full list per service):

| Variable | Used by | Description |
|---|---|---|
| `GCP_PROJECT_ID` | all services | Google Cloud project ID |
| `FIREBASE_CREDENTIALS` | all services | Path/JSON for Firebase service account |
| `PORT` | user, stream, admin, analytics | HTTP listen port |
| `GCS_BUCKET` / `CDN_BASE_URL` | stream-service | Media storage & delivery |
| `VIEWER_SUB` / `STREAM_SUB` | analytics-service | Pub/Sub subscription names |
| `NOTIFICATION_SUB` | notification-service | Pub/Sub subscription name |
| `ADMIN_PANEL_USERNAME` / `ADMIN_PANEL_PASSWORD` / `ADMIN_PANEL_SESSION_SECRET` | admin-service | Admin panel credentials & session secret |

## Deployment

- **GCP Cloud Run** (primary target): single container, all internal services started together, only the gateway bound to Cloud Run's `PORT`. See [`docs/gcp-cloud-run-deploy.md`](docs/gcp-cloud-run-deploy.md).
- **Cloud Build**: pipeline defined in `cloudbuild.yaml`.
- **Kubernetes**: alternative deployment manifests and horizontal pod autoscaling in `k8s/`.
- **Firebase Hosting**: static assets / custom domain setup, see [`docs/firebase-hosting-domain-setup.md`](docs/firebase-hosting-domain-setup.md).
- **Scripted deploy**: `scripts/deploy-all.sh`.

## Infrastructure

Infrastructure-as-code lives in [`infra/terraform/`](infra/terraform). See [`docs/build-vs-managed.md`](docs/build-vs-managed.md) and [`docs/pricing-scenarios.md`](docs/pricing-scenarios.md) for the reasoning behind managed-service choices and estimated cloud costs at different viewer scales. AWS was also evaluated — see [`docs/aws-migration-analysis.md`](docs/aws-migration-analysis.md).

## Testing

- **Load testing:** `k6 run k6-load-test.js`
- **Join-flow test:** `join_test.js`
- **Go unit tests:** `go test ./...`

## Admin & Public Assets

- `public/index.html` — landing page
- `public/dashboard.html` — analytics dashboard
- `public/upload.html` — video upload UI
- `public/admin/` — admin panel frontend
- `public/health/` — gateway health status page