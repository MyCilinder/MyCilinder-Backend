# Local secrets (Catalog)

Put the CloudFront signing private key here as `cloudfront.pem`. Everything in this
directory except this README is gitignored and excluded from Docker builds.

Compose mounts this directory read-only at `/run/secrets/catalog`, so set
`CLOUDFRONT_PRIVATE_KEY_FILE=/run/secrets/catalog/cloudfront.pem` in
`services/catalog/.env`. Keep the file readable only by you (`chmod 600`). On ECS the key
comes from Secrets Manager instead.
