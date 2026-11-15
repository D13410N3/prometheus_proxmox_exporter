#!/bin/sh
set -eu

IMAGE="${IMAGE:-zot.onlineteam.ru/proxmox-exporter}"
SERVICE="${SERVICE:-proxmox-exporter}"
CIVERSION_URL="${CIVERSION_URL:-https://civersion.onlineteam.ru}"

usage() {
  status="${1:-1}"
  cat >&2 <<EOF
Usage: $0 [major|minor|patch] | $0 <tag> | $0 --tag <tag>

  no arguments   ask civersion for the next version
  major|minor|patch  request a release type from civersion
  <tag>          build an explicit tag without asking civersion
  --tag <tag>    same as above, explicit form
EOF
  exit "$status"
}

RELEASE_TYPE=""
TAG=""

case "${1:-}" in
  "")
    ;;
  major|minor|patch)
    RELEASE_TYPE="$1"
    shift
    ;;
  --tag)
    [ "$#" -eq 2 ] || usage
    TAG="$2"
    shift 2
    ;;
  -h|--help)
    usage 0
    ;;
  --*)
    usage
    ;;
  *)
    TAG="$1"
    shift
    ;;
esac

[ "$#" -eq 0 ] || usage

if [ -z "$TAG" ]; then
  QUERY="service=$SERVICE&format=text"
  if [ -n "$RELEASE_TYPE" ]; then
    QUERY="$QUERY&type=$RELEASE_TYPE"
  fi

  RESPONSE_FILE="$(mktemp)"
  trap 'rm -f "$RESPONSE_FILE"' EXIT

  if ! HTTP_CODE="$(curl -sS --max-time 30 -o "$RESPONSE_FILE" -w '%{http_code}' "$CIVERSION_URL/api/version?$QUERY")"; then
    echo "Failed to reach civersion at $CIVERSION_URL" >&2
    exit 1
  fi
  if [ "$HTTP_CODE" != "200" ]; then
    echo "civersion request failed (HTTP $HTTP_CODE):" >&2
    cat "$RESPONSE_FILE" >&2
    echo >&2
    exit 1
  fi
  TAG="$(cat "$RESPONSE_FILE")"
fi

case "$TAG" in
  ""|[!A-Za-z0-9_]*|*[!A-Za-z0-9_.-]*)
    echo "Invalid Docker tag returned: '$TAG'" >&2
    exit 1
    ;;
esac

echo "Building $IMAGE:$TAG and $IMAGE:latest for linux/amd64"

docker buildx build \
  --platform linux/amd64 \
  -t "$IMAGE:$TAG" \
  -t "$IMAGE:latest" \
  --push \
  .
