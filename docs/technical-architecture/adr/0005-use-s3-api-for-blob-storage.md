# Use the S3 API as the Blob Store contract

Nano Notebook will store Source binaries and derived binary artifacts outside PostgreSQL behind the S3 API: AWS S3 is the production Blob Store, while local development uses MinIO through Docker Compose. Application code uses the same AWS SDK contract in both environments; because MinIO implements only a compatible subset, a real-S3 integration suite is required before production launch.
