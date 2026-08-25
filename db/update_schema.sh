#!/usr/bin/env bash
# Applies db/schema.sql declaratively with psqldef (sqldef). The file always
# describes the desired end state, there are no migration files. Dry-run by
# default so you can read the DDL diff before touching anything.
#
#   ./db/update_schema.sh              # dry-run: show what would change
#   ./db/update_schema.sh --apply      # apply the diff
#   ./db/update_schema.sh --drop       # allow DROP statements (with either mode)
set -euo pipefail

cd "$(dirname "$0")"

HOST="${CARSHARE_DB_HOST:-127.0.0.1}"
PORT="${CARSHARE_DB_PORT:-5434}"
USER="${CARSHARE_DB_USER:-postgres}"
PASSWORD="${CARSHARE_DB_PASSWORD:-carshare}"
DBNAME="${CARSHARE_DB_NAME:-carshare}"

PSQLDEF=./.psqldef
if [[ ! -x "${PSQLDEF}" ]]; then
  echo "downloading psqldef..." >&2
  curl -fsSL https://github.com/sqldef/sqldef/releases/latest/download/psqldef_linux_amd64.tar.gz \
    | tar -xzO psqldef > "${PSQLDEF}"
  chmod +x "${PSQLDEF}"
fi

ARGS=(--dry-run)
for flag in "$@"; do
  case "${flag}" in
    --apply) ARGS=("${ARGS[@]/--dry-run}") ;;
    --drop) ARGS+=(--enable-drop) ;;
    *) echo "unknown flag: ${flag}" >&2; exit 1 ;;
  esac
done

# Drop empty strings left by the --apply substitution.
CLEAN=()
for arg in "${ARGS[@]}"; do
  [[ -n "${arg}" ]] && CLEAN+=("${arg}")
done

PGPASSWORD="${PASSWORD}" "${PSQLDEF}" \
  --host "${HOST}" --port "${PORT}" --user "${USER}" \
  "${CLEAN[@]+"${CLEAN[@]}"}" "${DBNAME}" < schema.sql
