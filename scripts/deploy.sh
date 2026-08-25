#!/usr/bin/env bash
# Deploys the API: build the image, push it, point prod at it, wait for the
# rollout. One command, because a deploy split across four is a deploy where
# step three gets skipped and the running image no longer matches any commit.
#
#   ./scripts/deploy.sh            # build, push, apply (Terraform asks first)
#   ./scripts/deploy.sh -y         # same, no Terraform prompt
#   ./scripts/deploy.sh --plan     # build and push, then plan only
#   ./scripts/deploy.sh --rollback # put the previous tag back
#
# The tag is <utc timestamp>-<short sha>, so what is running always names the
# commit it came from. Tags are never reused, which is what makes --rollback
# work and what keeps Kubernetes from serving a stale layer for a moved tag.
#
# The web app deploys separately, on push to master (.github/workflows/
# web-deploy.yml). Deploy the API before pushing a web change that needs it.
set -euo pipefail

TFVARS="${CARSHARE_TFVARS:-${HOME}/.config/carshare/prod.tfvars}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

auto_approve=""
plan_only=""
rollback=""
for arg in "$@"; do
  case "${arg}" in
    -y|--yes) auto_approve="-auto-approve" ;;
    --plan) plan_only="1" ;;
    --rollback) rollback="1" ;;
    *) echo "unknown argument: ${arg}" >&2; exit 2 ;;
  esac
done

if [[ ! -f "${TFVARS}" ]]; then
  echo "no tfvars at ${TFVARS} (set CARSHARE_TFVARS to override)" >&2
  exit 1
fi

# tfvars holds every secret the stack needs, so it is read for two keys and
# never copied anywhere. `image` is the repository, `image_tag` is what prod
# currently runs.
tfvar() {
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\(.*\)\"[[:space:]]*$/\1/p" "${TFVARS}" | head -1
}

image="$(tfvar image)"
current_tag="$(tfvar image_tag)"
[[ -n "${image}" && -n "${current_tag}" ]] || { echo "tfvars is missing image or image_tag" >&2; exit 1; }

# Terraform state records the previous tag; the backup file is what --rollback
# reads, so it is written before every apply and only then.
previous_file="$(dirname "${TFVARS}")/.carshare-previous-image-tag"

if [[ -n "${rollback}" ]]; then
  [[ -f "${previous_file}" ]] || { echo "no previous tag recorded at ${previous_file}" >&2; exit 1; }
  target_tag="$(cat "${previous_file}")"
  echo "== rolling back ${current_tag} -> ${target_tag}"
else
  # A deploy has to name a commit that exists somewhere other than this laptop,
  # otherwise the running image cannot be rebuilt or reviewed later.
  if [[ -n "$(git status --porcelain -- ':!web')" ]]; then
    echo "refusing to deploy: uncommitted Go or Terraform changes" >&2
    git status --short -- ':!web' >&2
    exit 1
  fi
  head_sha="$(git rev-parse HEAD)"
  if ! git merge-base --is-ancestor "${head_sha}" "origin/master" 2>/dev/null; then
    echo "refusing to deploy: HEAD is not on origin/master (push first)" >&2
    exit 1
  fi
  target_tag="$(date -u +%Y.%m.%d_%H.%M.%S)-$(git rev-parse --short HEAD)"
  echo "== building ${image}:${target_tag}"
  docker build -t "${image}:${target_tag}" .
  echo "== pushing"
  docker push "${image}:${target_tag}"
fi

echo "== pointing prod at ${target_tag}"
echo "${current_tag}" > "${previous_file}"
# Rewrite in place through a temp file so an interrupted sed cannot leave the
# tfvars, which holds every secret, truncated.
tmp_tfvars="$(mktemp)"
trap 'rm -f "${tmp_tfvars}"' EXIT
sed "s|^\([[:space:]]*image_tag[[:space:]]*=[[:space:]]*\"\).*\(\"\)|\1${target_tag}\2|" "${TFVARS}" > "${tmp_tfvars}"
grep -q "\"${target_tag}\"" "${tmp_tfvars}" || { echo "failed to set image_tag in tfvars" >&2; exit 1; }
cat "${tmp_tfvars}" > "${TFVARS}"

cd terraform
terraform init -input=false >/dev/null
if [[ -n "${plan_only}" ]]; then
  terraform plan -var-file="${TFVARS}"
  echo "== plan only, prod unchanged (tfvars now says ${target_tag})"
  exit 0
fi
terraform apply -var-file="${TFVARS}" ${auto_approve}

# An apply that returns is not a rollout that succeeded: the new pods still
# have to pass their readiness probe, and a bad image fails here, not above.
kubeconfig="$(tfvar kubeconfig_path)"
namespace="$(tfvar namespace)"
if [[ -n "${kubeconfig}" && -n "${namespace}" ]]; then
  echo "== waiting for rollout"
  KUBECONFIG="${kubeconfig/#\~/${HOME}}" kubectl -n "${namespace}" rollout status deployment/carshare --timeout=5m
  echo "== running: $(KUBECONFIG="${kubeconfig/#\~/${HOME}}" kubectl -n "${namespace}" get deploy carshare -o jsonpath='{.spec.template.spec.containers[0].image}')"
fi
echo "== deployed ${target_tag} (previous ${current_tag}, restore with --rollback)"
