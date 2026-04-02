# Architecture Evolution (CCC'26)

This document captures architecture progression and target migration architecture for final jury submission.

## Initial to Current Evolution Summary

- Initial concept: mobile app with backend API.
- Current implemented architecture: gateway + multiple Go microservices + Pub/Sub + Firestore + Firebase Auth/FCM.
- Key change rationale: separate concerns, improve reliability, support burst traffic, and simplify public ingress.

## Current Implemented Architecture

```mermaid
flowchart LR
    A[Android App] --> G[API Gateway]
    G --> U[user-service]
    G --> S[stream-service]
    G --> N[notification-service]
    G --> X[analytics-service]
    G --> AD[admin-service]

    U --> FA[Firebase Auth]
    U --> FS[(Firestore)]
    S --> FS
    X --> FS
    AD --> FS

    S --> PS[(Pub/Sub Topics)]
    PS --> N
    N --> FCM[Firebase Cloud Messaging]
    FCM --> A

    X --> PM[Prometheus Metrics]
```

## Target AWS Migration Architecture

```mermaid
flowchart LR
    A[Android App] --> G[API Gateway / ALB]
    G --> U[user-service on ECS/EKS]
    G --> S[stream-service on ECS/EKS]
    G --> N[notification-service on ECS/EKS]
    G --> X[analytics-service on ECS/EKS]
    G --> AD[admin-service on ECS/EKS]

    U --> CG[Amazon Cognito]
    U --> DD[(DynamoDB)]
    S --> DD
    X --> DD
    AD --> DD

    S --> SNS[SNS]
    SNS --> SQS[SQS Subscriptions]
    SQS --> N
    N --> AP[Amazon SNS Mobile Push]
    AP --> A

    X --> CW[CloudWatch Metrics/Logs]
    S --> S3[(S3 Media)]
    S3 --> CF[CloudFront]
```
