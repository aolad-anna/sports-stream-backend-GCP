# Demo Evidence Checklist (CCC'26)

Use this checklist to close the remaining jury-proof tasks quickly.

## Must Attach

- [ ] Screenshot of k6 run summary at target concurrency.
- [ ] Screenshot of Terraform plan and apply output.
- [ ] Screenshot of created Pub/Sub topics/subscriptions in cloud console.
- [ ] Screenshot of Firestore rules publish success and access test results.
- [ ] Architecture diagram export included in final submission slide deck.

## Validation Steps

1. Run Firestore role tests for viewer, broadcaster, and admin identities.
2. Run Terraform apply in target project and save command output.
3. Run one end-to-end stream start event and verify notification delivery.
4. Run health checks for gateway and all services.
5. Verify admin login, CRUD pages, and logout in browser.

## External Repository Tasks

- [ ] Update Android BASE_URL to current backend URL in Android app repository.
- [ ] Build and test Android app against latest backend deployment.
