#!/usr/bin/env bash
set -euo pipefail

# =====================================================
# Local CI/CD Pipeline Runner using act
# Usage:
#   ./scripts/run-local.sh              # Run all jobs
#   ./scripts/run-local.sh lint-and-test # Run specific job
#   ./scripts/run-local.sh build        # Run specific job
# =====================================================

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

# ----- Pre-flight checks -----
info "Running pre-flight checks..."

command -v act    &>/dev/null || error "act is not installed. Run: brew install act"
command -v docker &>/dev/null || error "docker is not installed."
docker info &>/dev/null       || error "Docker daemon is not running. Start Docker Desktop first."

# ----- Start SonarQube if not running -----
SONAR_RUNNING=$(docker ps --filter "name=sonarqube" --format '{{.Names}}' 2>/dev/null || true)
if [ -z "$SONAR_RUNNING" ]; then
  info "Starting SonarQube on host (required for sonarqube job)..."
  docker compose -f docker-compose.sonarqube.yaml up -d
  info "SonarQube starting in background — it takes ~60s to boot."
  info "Waiting for SonarQube to be ready..."
  for i in $(seq 1 60); do
    STATUS=$(curl -s http://localhost:9000/api/system/status 2>/dev/null | grep -o '"status":"[^"]*"' | cut -d'"' -f4 || echo "")
    if [ "$STATUS" = "UP" ]; then
      info "SonarQube is ready!"
      break
    fi
    printf "  Waiting... (%d/60)\r" "$i"
    sleep 5
    if [ "$i" -eq 60 ]; then
      error "Timeout waiting for SonarQube."
    fi
  done
else
  info "SonarQube already running."
fi

# ----- Clean artifact directory -----
rm -rf /tmp/act-artifacts
mkdir -p /tmp/act-artifacts

# ----- Determine which job(s) to run -----
JOB_FLAG=""
if [ "${1:-}" != "" ]; then
  JOB_FLAG="-j $1"
  info "Running job: $1"
else
  info "Running full pipeline"
fi

# ----- Run act -----
info "Launching act..."
echo ""

act push \
  $JOB_FLAG \
  --env-file .github/act.env \
  --privileged

echo ""
info "Pipeline completed successfully!"
