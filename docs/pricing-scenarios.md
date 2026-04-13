# Pricing Scenarios (CCC'26)

This document provides estimated monthly cloud cost scenarios for the Sports Stream Platform.

## Assumptions

- Cloud provider baseline: GCP + Firebase managed services.
- Core stack: Firestore, Pub/Sub, Cloud Run-style compute, Cloud Storage egress, FCM (no direct per-message cost in typical use).
- Region: europe-west3 baseline estimate.
- Stream profile: peak-heavy sports events with burst traffic.
- These are planning estimates for jury discussion, not billing exports.

## Scenario Summary

| Scenario | Monthly Active Viewers | Peak Concurrent Viewers | Estimated Monthly Cost (USD) | Estimated Cost per Viewer (USD) |
|---|---:|---:|---:|---:|
| Idle | 0-50 | 0-10 | 35 | 0.70 (at 50 users) |
| Small | 1,000 | 300 | 220 | 0.22 |
| Medium | 10,000 | 2,000 | 1,650 | 0.165 |
| Large | 100,000 | 15,000 | 13,800 | 0.138 |

## Cost Breakdown by Service (Illustrative)

### Idle

- Compute and gateway baseline: 20
- Firestore reads/writes/storage baseline: 7
- Pub/Sub low-volume baseline: 3
- Monitoring/logging overhead: 5
- Total: 35

### 1K Users

- Compute and autoscaling runtime: 90
- Firestore operations and storage: 45
- Network egress and media delivery share: 55
- Pub/Sub + notifications: 15
- Observability + misc: 15
- Total: 220

### 10K Users

- Compute and autoscaling runtime: 620
- Firestore operations and storage: 320
- Network egress and media delivery share: 540
- Pub/Sub + notifications: 90
- Observability + misc: 80
- Total: 1,650

### 100K Users

- Compute and autoscaling runtime: 4,800
- Firestore operations and storage: 2,650
- Network egress and media delivery share: 5,500
- Pub/Sub + notifications: 550
- Observability + misc: 300
- Total: 13,800

## Notes for Jury

- Unit economics improve with scale due to shared platform overhead.
- Egress and streaming traffic dominate cost at larger workloads.
- Cost optimization priorities: caching strategy, adaptive bitrate policy, and burst-aware autoscaling.
- Final production values should be refreshed with billing export data before Demo Day.
