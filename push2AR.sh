#!/usr/bin/env bash
# Build the vigo server image and push it to Google Artifact Registry.
#
# Publishes two tags per run:
#   :<VERSION>   immutable, for rollback + audit
#   :latest      floating pointer — what `docker run ...:latest` picks up
#
# Customer install becomes a single command:
#   docker run -d --name vigo -p 8443:8443 -p 1530:1530 \
#     -v /srv/vigo:/srv/vigo \
#     us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest
#
# Override any of the environment defaults:
#   PROJECT=other-project REGION=us-central1 ./push2AR.sh
#
# One-time workstation setup:
#   gcloud auth login
#   gcloud auth configure-docker us-west1-docker.pkg.dev
#   gcloud artifacts repositories create vigo \
#     --repository-format=docker --location=us-west1
#   gcloud artifacts repositories add-iam-policy-binding vigo \
#     --location=us-west1 --member=allUsers \
#     --role=roles/artifactregistry.reader   # anonymous docker pull
#
# Flags:
#   --allow-dirty   push even when the working tree has uncommitted
#                   changes. Without this, the script aborts so the
#                   :<VERSION> tag always pins bytes that exist in a
#                   git commit.

set -euo pipefail

: "${PROJECT:=project-69f2499e-5082-48f0-b19}"
: "${REGION:=us-west1}"
: "${REPO:=vigo}"
: "${IMAGE_NAME:=vigo}"
: "${FLOATING_TAG:=latest}"

ALLOW_DIRTY=0
for arg in "$@"; do
    case "$arg" in
        --allow-dirty) ALLOW_DIRTY=1 ;;
        -h|--help)
            sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *)
            echo "unknown flag: $arg" >&2
            exit 2 ;;
    esac
done

ROOT="$(cd "$(dirname "$0")" && pwd)"
VERSION_FILE="${ROOT}/VERSION"
DOCKERFILE="${ROOT}/Dockerfile.server"

trap 'rc=$?; [[ $rc -ne 0 ]] && echo "push2AR.sh: failed at line ${LINENO} (exit ${rc})" >&2' EXIT

[[ -f "${VERSION_FILE}" ]] || { echo "missing VERSION at ${VERSION_FILE}" >&2; exit 1; }
[[ -f "${DOCKERFILE}" ]]   || { echo "missing ${DOCKERFILE}" >&2; exit 1; }

VERSION="$(tr -d '[:space:]' < "${VERSION_FILE}")"
[[ -n "${VERSION}" ]] || { echo "VERSION file is empty" >&2; exit 1; }

command -v gcloud >/dev/null || { echo "gcloud not in PATH" >&2; exit 1; }
command -v docker >/dev/null || { echo "docker not in PATH" >&2; exit 1; }

# Dirty-tree guard. The :<VERSION> tag must pin bytes that exist in a
# git commit — otherwise rollback by tag silently drops local-only
# changes and debug-by-git-blame lies. --allow-dirty opts out.
if git -C "${ROOT}" rev-parse --git-dir >/dev/null 2>&1; then
    if ! { git -C "${ROOT}" diff --quiet --ignore-submodules 2>/dev/null \
        && git -C "${ROOT}" diff --cached --quiet --ignore-submodules 2>/dev/null; }; then
        if [[ $ALLOW_DIRTY -eq 1 ]]; then
            echo "warning: working tree is dirty — ${VERSION} will include uncommitted changes" >&2
        else
            echo "working tree is dirty; the :${VERSION} tag would pin bytes that aren't in a commit." >&2
            echo "commit first, or re-run with --allow-dirty." >&2
            exit 1
        fi
    fi
fi

REGISTRY="${REGION}-docker.pkg.dev"
REPO_PATH="${REGISTRY}/${PROJECT}/${REPO}/${IMAGE_NAME}"
IMAGE_VERSIONED="${REPO_PATH}:${VERSION}"
IMAGE_FLOATING="${REPO_PATH}:${FLOATING_TAG}"

# Verify Docker knows how to auth against this AR host. gcloud's
# configure-docker writes the credential helper into Docker's config
# under the credHelpers map — string-match on the registry hostname.
if ! grep -q "\"${REGISTRY}\"" "${HOME}/.docker/config.json" 2>/dev/null; then
    echo "Docker is not configured to auth against ${REGISTRY}." >&2
    echo "Run once:  gcloud auth configure-docker ${REGISTRY}" >&2
    exit 1
fi

# Verify the AR repo exists; surfaces a friendly error instead of a
# push-time "repository not found" after a 5-minute build.
if ! gcloud artifacts repositories describe "${REPO}" \
        --project="${PROJECT}" --location="${REGION}" >/dev/null 2>&1; then
    echo "AR repo ${REPO} not found in ${PROJECT}/${REGION}." >&2
    echo "Create:  gcloud artifacts repositories create ${REPO} \\" >&2
    echo "           --repository-format=docker --location=${REGION} \\" >&2
    echo "           --project=${PROJECT}" >&2
    exit 1
fi

echo "Project:   ${PROJECT}"
echo "Region:    ${REGION}"
echo "Dockerfile:${DOCKERFILE}"
echo "Pushing:"
echo "  ${IMAGE_VERSIONED}"
echo "  ${IMAGE_FLOATING}"
echo

# --platform linux/amd64 guarantees the pushed image runs on customer
# x86 hosts regardless of what arch this script runs on (e.g. arm64 Mac).
# --pull refreshes FROM images each build so security fixes in base
# layers flow into every release instead of sticking on whatever was
# cached locally weeks ago.
echo "==> docker build"
docker build \
    --platform linux/amd64 \
    --pull \
    --file "${DOCKERFILE}" \
    --tag "${IMAGE_VERSIONED}" \
    --tag "${IMAGE_FLOATING}" \
    --build-arg "CACHEBUST=$(date +%s)" \
    "${ROOT}"

echo "==> docker push ${IMAGE_VERSIONED}"
docker push "${IMAGE_VERSIONED}"

echo "==> docker push ${IMAGE_FLOATING}"
docker push "${IMAGE_FLOATING}"

trap - EXIT

cat <<EOF

Pushed:
  ${IMAGE_VERSIONED}
  ${IMAGE_FLOATING}

Customer install:
  mkdir -p /srv/vigo
  docker run --rm -v /srv/vigo:/srv/vigo ${IMAGE_FLOATING} --seed-only
  cd /srv/vigo && docker compose up -d
EOF
