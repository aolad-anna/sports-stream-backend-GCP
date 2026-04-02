# Migration Analysis: GCP to AWS (CCC'26)

## Objective

Analyze what changes are required to migrate the Sports Stream Platform from GCP/Firebase-centric infrastructure to AWS.

## Service Mapping

| Current Platform (GCP/Firebase) | AWS Target | Migration Notes |
|---|---|---|
| Cloud Firestore | DynamoDB | Data model redesign for access patterns and secondary indexes. |
| Cloud Pub/Sub | SNS + SQS | Replace topic/subscription model with SNS fanout and SQS consumers. |
| Firebase Auth | Amazon Cognito | Migrate user identities and role claims. |
| Firebase Cloud Messaging | Amazon SNS Mobile Push | Reconfigure mobile push provider credentials and topics. |
| Cloud Run / container hosting | ECS Fargate or EKS | Keep container images; update deployment and scaling resources. |
| GCS media bucket | S3 + CloudFront | Migrate media objects and CDN URLs. |
| Cloud Monitoring/Logging | CloudWatch + X-Ray | Update metrics dashboards and alerting policies. |

## Architecture Changes Required

1. Identity and token validation middleware must support Cognito JWT issuer and keys.
2. Pub/Sub client package should be replaced with SNS/SQS adapters.
3. Firebase admin initialization in backend services must be replaced with AWS SDK clients.
4. Storage URLs and signed URL flow must be converted from GCS to S3 semantics.
5. Infra definitions should be moved from GCP resources to AWS resources in Terraform modules.

## Data Migration Plan

1. Export Firestore collections (users, streams, matches, analytics) to JSON/CSV.
2. Transform and load into DynamoDB tables with partition/sort key strategy.
3. Run dual-write period for selected entities to validate consistency.
4. Cut over read traffic service-by-service behind gateway feature flags.

## Operational Migration Plan

1. Establish AWS networking, IAM roles, and secret management.
2. Deploy gateway and one non-critical service in parallel (canary).
3. Migrate messaging pipeline (SNS/SQS) and notification service.
4. Migrate stateful data and update service endpoints.
5. Execute load test and rollback drill before full cutover.

## Risks and Mitigations

- Vendor API differences: isolate provider logic behind package interfaces.
- Authentication migration risk: run staged user migration and token validation fallback.
- Cost unpredictability during dual-run: define temporary budget alerts in both clouds.
- Delivery risk before Demo Day: prioritize architecture-level migration evidence if full runtime cutover is out of scope.
