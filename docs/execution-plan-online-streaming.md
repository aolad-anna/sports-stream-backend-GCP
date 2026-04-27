# Online Streaming Platform Execution Plan

Date: 2026-04-27
Owner: Backend Platform

## 1) Current Architecture Snapshot

- Entry point: gateway service routes to user, stream, notification, admin, analytics.
- Runtime model: all services can run as one container via start.sh, or as separate processes locally.
- Data/auth stack: Firebase Auth + Firestore.
- Event backbone: Pub/Sub topics for stream and viewer events; analytics and notification consume subscriptions.
- Admin/UI surfaces: gateway landing page + admin panel + health pages.
- Infra assets: docker-compose, Cloud Build deploy, Kubernetes manifests, Terraform for topics/subscriptions.

## 2) Main Risks To Address First

1. Duplicate Pub/Sub deliveries can skew analytics counters.
2. Firestore rules drift from code field names (broadcasterId vs broadcasterUid).
3. Missing automated regression tests for critical stream/analytics paths.
4. Limited observability (no end-to-end request correlation).
5. Secrets and deployment hardening still partially manual.

## 3) Execution Phases

### Phase A: Reliability Baseline (in progress)

1. Add idempotent event processing for analytics consumers.
2. Ensure publishers include stable event identifiers.
3. Validate build integrity with go test ./....

Success criteria:
- Duplicate viewer/stream events no longer double-apply.
- All Go packages compile after changes.

### Phase B: Security Alignment

1. Fix Firestore rules field mismatches and access rules for stream ownership.
2. Validate admin-only and role-based access paths against backend behavior.
3. Document deployment-time secret requirements and verification commands.

Success criteria:
- Rules match live document schema.
- No role bypass via mismatched field names.

### Phase C: Observability & Operability

1. Introduce request correlation IDs at gateway and forward to services.
2. Standardize structured JSON logs with service and request metadata.
3. Add a runbook for incident triage (health, Pub/Sub lag, Firestore issues).

Success criteria:
- One request traceable across gateway and service logs.
- Operational checks are reproducible from docs.

### Phase D: Quality Gates

1. Add unit tests for auth middleware and analytics event idempotency behavior.
2. Add CI task to run go test and optional smoke checks.
3. Parameterize load tests for staging/prod with environment variables.

Success criteria:
- Automated tests protect auth/event regressions.
- Load test command can run non-interactively.

### Phase E: Scale & Cost Controls

1. Review Firestore hotspot risk on analytics docs and tune update strategy.
2. Align HPA/K8s manifests with actual deployment target.
3. Publish cost/scaling dashboard checklist for release readiness.

Success criteria:
- Bottleneck mitigation strategy documented and partially implemented.
- Deployment manifests represent real operational path.

## 4) Task Tracker

- [x] A1 Add idempotent event processing design.
- [x] A2 Add eventId publishing from stream-service events.
- [x] A3 Validate Go compile status with go test ./....
- [x] B1 Update Firestore rules schema alignment.
- [ ] B2 Verify role-based access paths.
- [x] C1 Add request correlation IDs.
- [x] C2 Add structured log helpers.
- [x] D1 Add analytics idempotency baseline tests.
- [x] D2 Add auth middleware tests.

## 5) Change Strategy

- Keep changes incremental and compile-safe.
- Prefer minimal invasive edits in core services.
- Run gofmt and go test after each task group.
- Avoid unrelated refactors while hardening critical paths.
