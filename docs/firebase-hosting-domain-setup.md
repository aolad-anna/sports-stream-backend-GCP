# Firebase Hosting domain setup for livestream.study

This repository is now configured so Firebase Hosting can front the Cloud Run backend service named sports-stream-backend-staging in region europe-west1.

## What this gives you

- livestream.study serves over HTTPS
- Firebase Hosting acts as the public front door
- all requests are proxied to the Cloud Run backend
- you still manage the backend in Google Cloud Console

## One-time setup

1. Install Firebase CLI
   npm install -g firebase-tools

2. Sign in
   firebase login

3. Select the project from this repo root
   firebase use sports-stream-66553

4. Deploy hosting
   firebase deploy --only hosting

## Connect the custom domain

1. Open Firebase Console
2. Go to Hosting
3. Click Add custom domain
4. Enter livestream.study
5. Add the DNS records Firebase gives you at your domain registrar
6. Wait for certificate provisioning

## Validation

After DNS finishes, test:

- https://livestream.study/
- https://livestream.study/health
- https://livestream.study/admin/login

## Important note

The rewrite in firebase.json points to the Cloud Run service:
- serviceId: sports-stream-backend-staging
- region: europe-west1

If your actual Cloud Run service name changes, update firebase.json before deploying Hosting again.
