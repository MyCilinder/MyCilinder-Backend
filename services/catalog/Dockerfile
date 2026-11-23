# Catalog service image. Build from the repository root, because the service depends on
# libs/go through a replace directive:
#
#   docker build -f services/catalog/Dockerfile -t mycilinder-catalog .
#
# The same image runs locally (Compose) and on ECS; only the environment differs (see
# services/catalog/.env.example and internal/config).

FROM golang:1.25 AS build
WORKDIR /src

# Module files first, so dependency downloads are cached across code changes.
COPY libs/go/go.mod libs/go/go.sum libs/go/
COPY services/catalog/go.mod services/catalog/go.sum services/catalog/
WORKDIR /src/services/catalog
ENV GOWORK=off
RUN go mod download

COPY libs/go /src/libs/go
COPY services/catalog /src/services/catalog
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/catalog ./cmd/catalog

# Distroless static: no shell or package manager, CA certificates included (needed for
# Cognito JWKS, RDS Proxy, ElastiCache TLS, SQS, and S3), runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/catalog /catalog
USER nonroot:nonroot
EXPOSE 8001
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/catalog", "healthcheck"]
ENTRYPOINT ["/catalog"]
