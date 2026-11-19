# Mock API image (development only). Build from the repository root:
#
#   docker build -f deploy/local/mock-api/Dockerfile -t mycilinder-mock-api .
#
# The media stage downloads a few short 720p MP4 clips and placeholder thumbnails once,
# at build time, so the demo does not depend on those sites while it runs.

FROM curlimages/curl:8.11.1 AS media
USER root
WORKDIR /media
# Clips: Big Buck Bunny, Sintel (Blender Foundation, CC BY), Jellyfish (test-videos.co.uk),
# Flower (MDN, CC0). "file seconds" per line in clips.txt.
RUN set -eu; \
    get() { curl -fsSL --retry 3 --max-time 120 -o "$1" "$2"; }; \
    get bbb.mp4       https://test-videos.co.uk/vids/bigbuckbunny/mp4/h264/720/Big_Buck_Bunny_720_10s_2MB.mp4; \
    get jellyfish.mp4 https://test-videos.co.uk/vids/jellyfish/mp4/h264/720/Jellyfish_720_10s_2MB.mp4; \
    get sintel.mp4    https://test-videos.co.uk/vids/sintel/mp4/h264/720/Sintel_720_10s_2MB.mp4; \
    get flower.mp4    https://interactive-examples.mdn.mozilla.net/media/cc0-videos/flower.mp4; \
    printf 'bbb.mp4 10\njellyfish.mp4 10\nsintel.mp4 10\nflower.mp4 5\n' > clips.txt; \
    mkdir thumbs; \
    for i in 01 02 03 04 05 06 07 08 09 10 11 12; do \
      get "thumbs/$i.jpg" "https://picsum.photos/seed/mycilinder$i/640/360"; \
    done; \
    ls -la . thumbs

FROM golang:1.25 AS build
WORKDIR /src
COPY libs/go/go.mod libs/go/go.sum libs/go/
COPY deploy/local/mock-api/go.mod deploy/local/mock-api/go.sum deploy/local/mock-api/
WORKDIR /src/deploy/local/mock-api
ENV GOWORK=off
RUN go mod download
COPY libs/go /src/libs/go
COPY deploy/local/mock-api /src/deploy/local/mock-api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mock-api .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=media /media /media
COPY --from=build /out/mock-api /mock-api
USER nonroot:nonroot
EXPOSE 8000
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 CMD ["/mock-api", "healthcheck"]
ENTRYPOINT ["/mock-api"]
