#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# collect-results.sh
# Runs all tests locally and collects results into results/
# ============================================================

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RESULTS_DIR="$ROOT_DIR/results"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

info()  { echo -e "${GREEN}[OK]${NC} $1"; }
warn()  { echo -e "${YELLOW}[SKIP]${NC} $1"; }
fail()  { echo -e "${RED}[FAIL]${NC} $1"; }

SUMMARY=()

# ---- Setup ----
echo "============================================================"
echo "  Collecting test results into results/"
echo "============================================================"
echo ""

rm -rf "$RESULTS_DIR"
mkdir -p "$RESULTS_DIR"/{unit-test,sast,load-test,chaos-test,prometheus,sonarqube}

# ---- 1. Unit Tests with Coverage ----
echo "--- Unit Tests ---"
for svc in api worker; do
  if [ -d "$ROOT_DIR/$svc" ]; then
    echo "Running tests for $svc..."
    pushd "$ROOT_DIR/$svc" > /dev/null
    if go test -v -race -coverprofile="$RESULTS_DIR/unit-test/${svc}-coverage.out" ./... > "$RESULTS_DIR/unit-test/${svc}-test-output.log" 2>&1; then
      go tool cover -html="$RESULTS_DIR/unit-test/${svc}-coverage.out" -o "$RESULTS_DIR/unit-test/${svc}-coverage.html"
      go tool cover -func="$RESULTS_DIR/unit-test/${svc}-coverage.out" > "$RESULTS_DIR/unit-test/${svc}-coverage-func.txt"
      COVERAGE=$(tail -1 "$RESULTS_DIR/unit-test/${svc}-coverage-func.txt" | awk '{print $3}')
      info "$svc coverage: $COVERAGE"
      SUMMARY+=("Unit test ($svc): PASS - coverage $COVERAGE")
    else
      fail "$svc tests failed"
      SUMMARY+=("Unit test ($svc): FAIL (see $RESULTS_DIR/unit-test/${svc}-test-output.log)")
    fi
    popd > /dev/null
  else
    warn "$svc directory not found"
    SUMMARY+=("Unit test ($svc): SKIP - directory not found")
  fi
done
echo ""

# ---- 2. SAST (gosec) ----
echo "--- SAST (gosec) ---"
if command -v gosec &> /dev/null; then
  for svc in api worker; do
    if [ -d "$ROOT_DIR/$svc" ]; then
      echo "Running gosec on $svc..."
      pushd "$ROOT_DIR/$svc" > /dev/null
      if gosec -fmt=json -out="$RESULTS_DIR/sast/gosec-${svc}-report.json" -stdout -verbose=text ./... 2>&1; then
        info "gosec $svc scan complete"
        SUMMARY+=("SAST ($svc): PASS")
      else
        warn "gosec $svc found issues (report saved)"
        SUMMARY+=("SAST ($svc): ISSUES FOUND (see report)")
      fi
      popd > /dev/null
    fi
  done
else
  warn "gosec not installed (install: go install github.com/securego/gosec/v2/cmd/gosec@latest)"
  SUMMARY+=("SAST: SKIP - gosec not installed")
fi
echo ""

# ---- 3. Load Tests (k6) ----
echo "--- Load Tests (k6) ---"
if command -v k6 &> /dev/null; then
  BASE_URL="${BASE_URL:-http://localhost:8080}"
  # Check if the service is reachable
  if curl -s --max-time 3 "$BASE_URL/live" > /dev/null 2>&1; then
    if [ -f "$ROOT_DIR/k6/load-test.js" ]; then
      echo "Running k6 load test..."
      if k6 run --out json="$RESULTS_DIR/load-test/k6-load-test.json" -e BASE_URL="$BASE_URL" "$ROOT_DIR/k6/load-test.js" 2>&1; then
        info "k6 load test complete"
        SUMMARY+=("Load test: PASS")
      else
        fail "k6 load test failed"
        SUMMARY+=("Load test: FAIL")
      fi
    fi
    if [ -f "$ROOT_DIR/k6/stress-test.js" ]; then
      echo "Running k6 stress test..."
      if k6 run --out json="$RESULTS_DIR/load-test/k6-stress-test.json" -e BASE_URL="$BASE_URL" "$ROOT_DIR/k6/stress-test.js" 2>&1; then
        info "k6 stress test complete"
        SUMMARY+=("Stress test: PASS")
      else
        fail "k6 stress test failed"
        SUMMARY+=("Stress test: FAIL")
      fi
    fi
  else
    warn "Service not reachable at $BASE_URL (start with docker compose up)"
    SUMMARY+=("Load test: SKIP - service not reachable")
  fi
else
  warn "k6 not installed"
  SUMMARY+=("Load test: SKIP - k6 not installed")
fi
echo ""

# ---- 4. Prometheus Metrics Snapshot ----
echo "--- Prometheus Metrics ---"
PROM_URL="${PROM_URL:-http://localhost:9090}"
if curl -s --max-time 3 "$PROM_URL/-/ready" > /dev/null 2>&1; then
  echo "Scraping Prometheus metrics..."
  curl -s "$PROM_URL/api/v1/targets" > "$RESULTS_DIR/prometheus/prometheus-snapshot.json" 2>/dev/null
  info "Prometheus snapshot saved"
  SUMMARY+=("Prometheus: COLLECTED")
else
  warn "Prometheus not reachable at $PROM_URL"
  SUMMARY+=("Prometheus: SKIP - not reachable")
fi
echo ""

# ---- 5. Chaos Test Results ----
echo "--- Chaos Test Results ---"
CHAOS_FOUND=false
if command -v kubectl &> /dev/null; then
  # Try to get chaos results from any namespace
  for ns in agnos-dev agnos-uat; do
    CHAOS_JSON=$(kubectl get chaosresult -n "$ns" -o json 2>/dev/null || echo "")
    if [ -n "$CHAOS_JSON" ] && echo "$CHAOS_JSON" | grep -q '"items"'; then
      ITEM_COUNT=$(echo "$CHAOS_JSON" | grep -o '"name"' | wc -l)
      if [ "$ITEM_COUNT" -gt 0 ]; then
        echo "$CHAOS_JSON" > "$RESULTS_DIR/chaos-test/chaos-results.json"
        info "Chaos results collected from $ns ($ITEM_COUNT results)"
        SUMMARY+=("Chaos test: COLLECTED from $ns")
        CHAOS_FOUND=true
        break
      fi
    fi
  done
else
  warn "kubectl not installed"
fi

# Fallback: Check if act has already collected chaos results
if [ "$CHAOS_FOUND" = "false" ] && [ -f "/tmp/act-results/chaos-test/chaos-results.json" ]; then
  cp "/tmp/act-results/chaos-test/chaos-results.json" "$RESULTS_DIR/chaos-test/"
  info "Chaos results collected from act pipeline cache"
  SUMMARY+=("Chaos test: COLLECTED from act cache")
  CHAOS_FOUND=true
fi

if [ "$CHAOS_FOUND" = "false" ]; then
  warn "No chaos test results found in cluster or act cache"
  SUMMARY+=("Chaos test: SKIP - no results found")
fi
echo ""

# ---- 6. SonarQube Report ----
echo "--- SonarQube Report ---"
SONAR_URL="${SONAR_URL:-http://localhost:9000}"
SONAR_PROJECT="${SONAR_PROJECT:-agnos-devops}"
if curl -s --max-time 3 "$SONAR_URL/api/system/status" > /dev/null 2>&1; then
  echo "Fetching SonarQube report..."
  curl -s "$SONAR_URL/api/measures/component?component=${SONAR_PROJECT}&metricKeys=coverage,bugs,vulnerabilities,code_smells,duplicated_lines_density" \
    > "$RESULTS_DIR/sonarqube/sonarqube-report.json" 2>/dev/null
  info "SonarQube report saved"
  SUMMARY+=("SonarQube: COLLECTED")
else
  warn "SonarQube not reachable at $SONAR_URL"
  SUMMARY+=("SonarQube: SKIP - not reachable")
fi
echo ""

# ---- Remove empty directories ----
find "$RESULTS_DIR" -type d -empty -delete 2>/dev/null || true

# ---- Summary ----
echo "============================================================"
echo "  Results Summary"
echo "============================================================"
for item in "${SUMMARY[@]}"; do
  echo "  - $item"
done
echo ""
echo "Results directory: $RESULTS_DIR"
echo ""
ls -R "$RESULTS_DIR" 2>/dev/null || true
echo ""
echo "Done."
