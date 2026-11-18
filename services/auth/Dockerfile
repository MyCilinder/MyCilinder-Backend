# Auth facade image. Build from the repository root, because the service depends on
# libs/go through a replace directive:
#
#   docker build -f services/auth/Dockerfile -t mycilinder-auth .

FROM golang:1.25 AS build
WORKDIR /src

# Module files first, so dependency downloads are cached across code changes.
COPY libs/go/go.mod libs/go/go.sum libs/go/
COPY services/auth/go.mod services/auth/go.sum services/auth/
WORKDIR /src/services/auth
ENV GOWORK=off
RUN go mod download

COPY libs/go /src/libs/go
COPY services/auth /src/services/auth
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/auth ./cmd/auth

# Distroless static: no shell or package manager, CA certificates included (needed for
# Cognito, RDS Proxy, and ElastiCache TLS), runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/auth /auth
USER nonroot:nonroot
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/auth", "healthcheck"]
ENTRYPOINT ["/auth"]
