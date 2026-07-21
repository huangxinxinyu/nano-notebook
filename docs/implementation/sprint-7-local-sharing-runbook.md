# Sprint 7 Local Sharing Runbook

Run `scripts/start` to start Nano Notebook and its local-only mail capture service.

- Web application: `http://localhost:5173`
- Mailpit mailbox: `http://localhost:58025`
- SMTP capture: `127.0.0.1:51025`

Create an Invitation from **Manage access**, then open Mailpit to follow the one-time acceptance link. Mail never leaves the local machine. The Worker uses `NANO_MAIL_SMTP_ADDR`, `NANO_MAIL_FROM`, and `NANO_WEB_BASE_URL`; all three have local defaults and can be overridden before running `scripts/start`.

Invitation links expire after seven days. Revoking or rotating an Invitation invalidates older links even if an older captured email remains visible in Mailpit.
