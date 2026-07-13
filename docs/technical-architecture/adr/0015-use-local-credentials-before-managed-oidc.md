# Use Local Credentials before managed OIDC

Local product development will implement a bounded email-and-password credential mode so account, sharing, permission, and session flows can run without an external identity service. Internal User identity remains separate from credentials, and production launch is blocked until a later deployment Sprint adds a managed, provider-neutral OIDC adapter and disables Local Credential registration and login before public internet exposure; Local Credentials are not a production authentication commitment.
