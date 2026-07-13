# Use revocable opaque application Sessions

Nano Notebook will issue random opaque Session cookies whose hashed server-side records live in PostgreSQL, rather than using self-contained JWTs as application login state. This keeps logout, password changes, membership revocation, and account deletion immediately manageable in the single Control Plane, while a later OIDC login can map to the same internal User and Session mechanism without changing authorization.
