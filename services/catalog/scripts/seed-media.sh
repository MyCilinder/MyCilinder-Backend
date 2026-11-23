#!/usr/bin/env bash
# Uploads test HLS media for seeded READY videos to the dev hls-output bucket, so signed
# playback can be tried end to end (Catalog plan, Task 12). Local development only.
#
#   services/catalog/scripts/seed-media.sh [videoId ...]
#
# Default ids: seed videos 01 and 02. For each id it writes, in this order:
#   thumbnails/{id}.jpg
#   videos/{id}/360p/index.m3u8 + segments, videos/{id}/720p/index.m3u8 + segments
#   videos/{id}/master.m3u8   (last: the master playlist is the commit marker)
#
# Needs the aws CLI and either ffmpeg or Docker (it then runs ffmpeg in a container).
# Reads HLS_OUTPUT_BUCKET and AWS_PROFILE from services/catalog/.env unless they are
# already set. The profile needs s3:PutObject on the bucket.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
env_file="$here/../.env"
if [[ -f "$env_file" ]]; then
  # Only the variables this script needs; values already in the environment win.
  for v in HLS_OUTPUT_BUCKET AWS_PROFILE AWS_REGION; do
    if [[ -z "${!v:-}" ]]; then
      line="$(grep -E "^${v}=" "$env_file" | tail -1 || true)"
      [[ -n "$line" ]] && export "$v=${line#*=}"
    fi
  done
fi
: "${HLS_OUTPUT_BUCKET:?set HLS_OUTPUT_BUCKET (services/catalog/.env)}"

ids=("$@")
if [[ ${#ids[@]} -eq 0 ]]; then
  ids=(5eed1000-0000-4000-8000-000000000001 5eed1000-0000-4000-8000-000000000002)
fi
for id in "${ids[@]}"; do
  [[ "$id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || { echo "not a lower-case UUID: $id" >&2; exit 1; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if command -v ffmpeg >/dev/null 2>&1; then
  ff() { (cd "$work" && ffmpeg -hide_banner -loglevel error -y "$@"); }
else
  command -v docker >/dev/null 2>&1 || { echo "install ffmpeg or Docker" >&2; exit 1; }
  image="${FFMPEG_IMAGE:-jrottenberg/ffmpeg:7.1-alpine}"
  ff() { docker run --rm -u "$(id -u):$(id -g)" -v "$work:/work" -w /work "$image" -hide_banner -loglevel error -y "$@"; }
fi

for id in "${ids[@]}"; do
  echo "== $id"
  out="$work/$id"
  mkdir -p "$out/360p" "$out/720p"
  rel="$id"

  # 10 s test pattern with a tone. Keyframes every 2 s so segments split cleanly.
  for r in 360p:640x360:800k 720p:1280x720:2500k; do
    IFS=: read -r name size rate <<<"$r"
    ff -f lavfi -i "testsrc2=size=${size}:rate=30:duration=10" \
       -f lavfi -i "sine=frequency=440:duration=10" \
       -vf "drawtext=text='${id:32:4} ${name}':fontsize=48:fontcolor=white:x=20:y=20:box=1:boxcolor=black@0.6" \
       -c:v libx264 -profile:v main -preset veryfast -b:v "$rate" -g 60 -keyint_min 60 -sc_threshold 0 \
       -c:a aac -b:a 96k -ac 2 \
       -f hls -hls_time 4 -hls_playlist_type vod \
       -hls_segment_filename "$rel/$name/seg_%03d.ts" "$rel/$name/index.m3u8" 2>/dev/null \
    || ff -f lavfi -i "testsrc2=size=${size}:rate=30:duration=10" \
       -f lavfi -i "sine=frequency=440:duration=10" \
       -c:v libx264 -profile:v main -preset veryfast -b:v "$rate" -g 60 -keyint_min 60 -sc_threshold 0 \
       -c:a aac -b:a 96k -ac 2 \
       -f hls -hls_time 4 -hls_playlist_type vod \
       -hls_segment_filename "$rel/$name/seg_%03d.ts" "$rel/$name/index.m3u8"
  done
  ff -f lavfi -i "testsrc2=size=1280x720:rate=1:duration=1" -frames:v 1 -q:v 3 "$rel/thumb.jpg"

  cat > "$out/master.m3u8" <<'M3U8'
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
360p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2700000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2"
720p/index.m3u8
M3U8

  s3="s3://$HLS_OUTPUT_BUCKET"
  aws s3 cp "$out/thumb.jpg" "$s3/thumbnails/$id.jpg" --content-type image/jpeg --only-show-errors
  for name in 360p 720p; do
    aws s3 cp "$out/$name/" "$s3/videos/$id/$name/" --recursive --exclude '*' --include '*.ts' \
      --content-type video/mp2t --only-show-errors
    aws s3 cp "$out/$name/index.m3u8" "$s3/videos/$id/$name/index.m3u8" \
      --content-type application/vnd.apple.mpegurl --only-show-errors
  done
  aws s3 cp "$out/master.m3u8" "$s3/videos/$id/master.m3u8" \
    --content-type application/vnd.apple.mpegurl --only-show-errors
  echo "   uploaded $(find "$out" -type f ! -name thumb.jpg | wc -l) HLS files and the thumbnail"
done
