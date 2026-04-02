# CCC'26 Requirement Matrix

This matrix maps each jury requirement to current status and repository evidence.

## Core Requirements

| Requirement | Status | Evidence |
|---|---|---|
| Identity and Access Management | Done | `pkg/middleware/auth.go`, `services/user/main.go` |
| Multiple UIs (normal + admin) | Done (backend scope) | `services/admin/main.go`, Android UI referenced in report |
| Infrastructure as Code | Partial | `infra/terraform/`, `k8s/deployments.yaml`, `k8s/hpa.yaml` |
| Security best practices | Partial | `firestore.rules`, `docs/security-cleanup.md`, `services/admin/main.go` |
| Build vs Managed rationale | Done | `docs/build-vs-managed.md` |
| Architecture planning/evolution | Done | `docs/architecture-evolution.md`, `implementation-report-ccc26.md` |
| Scalability and load testing | Partial | `k6-load-test.js`, `k8s/hpa.yaml` (evidence attachment pending) |
| Monitoring and observability | Done | `services/analytics/main.go`, gateway and service health endpoints |
| Pricing scenarios | Done | `docs/pricing-scenarios.md` |
| Migration to other provider | Done | `docs/aws-migration-analysis.md` |

## Operational Readiness Checks

| Area | Status | Evidence |
|---|---|---|
| Gateway aggregate health with timeouts | Done | `gateway/main.go` |
| Service dependency health checks | Done | `services/user/main.go`, `services/stream/main.go`, `services/analytics/main.go`, `services/notification/main.go`, `services/admin/main.go` |
| Admin panel auth hardening | Done | `services/admin/main.go` |
| CI build verification | Done | `.github/workflows/deploy.yml` |
| Terraform validation workflow | Done | `.github/workflows/terraform.yml` |
| Secure secret handling baseline | Done | `.gitignore`, `.env.example`, `firebase-credentials.example.json` |

## Remaining External Actions (Not Finishable Inside This Repo Alone)

1. Apply and verify Firestore rules in Firebase project.
2. Rotate previously exposed Firebase service account key and update secrets.
3. Run Terraform apply in target cloud project and collect output evidence.
4. Attach load test screenshots/results to final submission package.
5. Update Android `BASE_URL` in Android repository and validate end-to-end.
