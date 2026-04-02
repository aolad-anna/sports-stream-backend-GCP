# Security Cleanup Notes

## Changes Applied

- Removed committed Firebase service account key file from repository.
- Added `.gitignore` rules to prevent committing local credentials and `.env`.
- Added `firebase-credentials.example.json` as a safe template.
- Hardened admin panel runtime to require explicit secure credentials in production.

## Required Follow-Up (Critical)

1. Rotate the previously exposed Firebase service account key in GCP IAM.
2. Update deployment secrets with the new credential JSON.
3. Verify all services start using the new secret source.

## Recommended Secret Handling

- Keep service account JSON only in secret manager or private deployment environment.
- Do not commit secrets to git history.
- Use `FIREBASE_CREDENTIALS` env var as raw JSON or mounted secret path.
