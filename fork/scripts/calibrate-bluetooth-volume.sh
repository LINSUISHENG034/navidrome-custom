#!/usr/bin/env bash
set -euo pipefail

CONTAINER="${CONTAINER:-navidrome}"
UI_VOLUME_PERCENT="${UI_VOLUME_PERCENT:-50}"
HOST_SINK_VOLUME="${HOST_SINK_VOLUME:-100%}"
HOST_SINK="${HOST_SINK:-$(pactl get-default-sink)}"

usage() {
  cat <<'EOF'
Usage:
  calibrate-bluetooth-volume.sh --exp 0.3
  calibrate-bluetooth-volume.sh --series 0.5,0.4,0.3,0.25,0.2

Environment:
  CONTAINER           Docker container name. Default: navidrome
  UI_VOLUME_PERCENT   Simulated UI slider value. Default: 50
  HOST_SINK           Host PulseAudio/PipeWire sink name. Default: current default sink
  HOST_SINK_VOLUME    Host sink volume used during calibration. Default: 100%
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

ensure_container_socat() {
  docker exec "${CONTAINER}" sh -lc 'command -v socat >/dev/null 2>&1 || apk add --no-cache socat >/dev/null'
}

find_mpv_socket() {
  docker exec "${CONTAINER}" sh -lc \
    "find /tmp -maxdepth 1 -type s -name 'mpv-ctrl-*.socket' | head -1"
}

set_host_sink() {
  pactl set-sink-volume "${HOST_SINK}" "${HOST_SINK_VOLUME}"
}

calc_gain() {
  local exponent="$1"
  awk -v ui="${UI_VOLUME_PERCENT}" -v exponent="${exponent}" \
    'BEGIN { printf "%.6f", ((ui / 100.0) ^ exponent) }'
}

calc_mpv_volume() {
  local exponent="$1"
  awk -v ui="${UI_VOLUME_PERCENT}" -v exponent="${exponent}" \
    'BEGIN { printf "%d", (((ui / 100.0) ^ exponent) * 100) }'
}

apply_exponent() {
  local exp="$1"
  local socket="$2"
  local gain
  local mpv_volume

  gain="$(calc_gain "${exp}")"
  mpv_volume="$(calc_mpv_volume "${exp}")"

  printf '{"command":["set_property","volume",%s]}\n' "${mpv_volume}" |
    docker exec -i "${CONTAINER}" sh -lc "socat - UNIX-CONNECT:${socket}" >/dev/null

  printf 'Applied exp=%s ui=%s%% gain=%s mpv_volume=%s host_sink=%s host_sink_volume=%s\n' \
    "${exp}" "${UI_VOLUME_PERCENT}" "${gain}" "${mpv_volume}" "${HOST_SINK}" "${HOST_SINK_VOLUME}"
}

run_series() {
  local series="$1"
  local socket="$2"
  IFS=',' read -r -a exponents <<<"${series}"

  for exp in "${exponents[@]}"; do
    apply_exponent "${exp}" "${socket}"
    printf 'Judge this loudness, then press Enter for next value... '
    read -r _
  done
}

main() {
  require_cmd docker
  require_cmd pactl
  require_cmd awk

  local mode=""
  local value=""

  while (($# > 0)); do
    case "$1" in
      --exp)
        mode="exp"
        value="${2:-}"
        shift 2
        ;;
      --series)
        mode="series"
        value="${2:-}"
        shift 2
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "Unknown argument: $1" >&2
        usage >&2
        exit 1
        ;;
    esac
  done

  if [[ -z "${mode}" || -z "${value}" ]]; then
    usage >&2
    exit 1
  fi

  set_host_sink
  ensure_container_socat

  local socket
  socket="$(find_mpv_socket)"
  if [[ -z "${socket}" ]]; then
    echo "No active mpv IPC socket found in container ${CONTAINER}" >&2
    exit 1
  fi

  if [[ "${mode}" == "exp" ]]; then
    apply_exponent "${value}" "${socket}"
  else
    run_series "${value}" "${socket}"
  fi
}

main "$@"
