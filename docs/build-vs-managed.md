# Build vs Managed Decisions (CCC'26)

## Decision Framework

For each component, we evaluated:

- Product differentiation value
- Operational burden and reliability requirements
- Team capacity and delivery timeline
- Security and compliance risk

## Decisions

| Component | Decision | Rationale |
|---|---|---|
| User authentication | Managed (Firebase Auth) | Reliable identity flows with low setup time and secure token lifecycle. |
| Notification delivery | Managed (FCM) | Commodity capability; managed service reduces operational complexity. |
| Event bus | Managed (Pub/Sub) | Durable asynchronous communication without operating brokers. |
| NoSQL data store | Managed (Firestore) | Fast development and global managed operations for document workloads. |
| API gateway logic | Build | Custom route policy, service aggregation, and competition-specific health endpoints. |
| Domain services (user, stream, notification, analytics, admin) | Build | Core product behavior and business logic are differentiators. |
| Pre-scaler policy | Build | Demand-aware scaling behavior tailored to sports-event traffic spikes. |
| Container runtime platform | Managed where available | Platform-managed compute chosen to reduce ops burden. |

## Tradeoff Summary

- Managed services accelerated implementation and improved reliability baseline.
- Custom-built services preserved flexibility for domain behavior and jury demo control.
- The hybrid approach balanced innovation speed and production stability.
