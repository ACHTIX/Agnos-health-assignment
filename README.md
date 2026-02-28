# Agnos DevOps Assignment

A production-ready DevOps setup with two independent Go microservices, CloudNativePG HA database, Docker containerization, Kubernetes orchestration across 3 environments (DEV/UAT/PROD), and a **3-branch CI/CD pipeline** (`dev` → `uat` → `prod`) with environment isolation and manual approval gates.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│                     Kind Kubernetes Cluster                       │
│              (1 control-plane + 3 worker nodes)                  │
│                                                                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐              │
│  │  agnos-dev   │  │  agnos-uat   │  │  agnos-prod  │             │
│  │  1 API       │  │  3 API       │  │  3 API       │             │
│  │  1 Worker    │  │  2 Worker    │  │  2 Worker     │             │
│  │  2 Postgres  │  │  3 Postgres  │  │  3 Postgres   │             │
│  │  (CNPG HA)   │  │  (CNPG HA)   │  │  (CNPG HA)    │             │
│  └─────────────┘  └─────────────┘  └─────────────┘              │
│                                                                   │
│  Each namespace contains:                                         │
│  ConfigMap, Secret, Deployments, Services, HPA, PDB,             │
│  NetworkPolicy, Ingress, LimitRange, ResourceQuota               │
└──────────────────────────────────────────────────────────────────┘
```

### Services

| Service | Description | Port | Endpoints |
|---------|-------------|------|-----------|
| **API** | HTTP API server with health checks and Prometheus metrics | 8080 (internal), 8090 (public) | `GET /live`, `GET /ready`, `GET /metrics`, `GET /api/v1/time` (public) |
| **Worker** | Background service that periodically processes batches from PostgreSQL | 8081 | `GET /live`, `GET /ready`, `GET /metrics` |
| **PostgreSQL** | CloudNativePG HA cluster with streaming replication and auto-failover | 5432 | — |

---

## Project Structure

Each service is a **fully independent microservice** with its own `go.mod`, dependencies, and Dockerfile.

```
.
├── api/                              # API microservice
│   ├── main.go                       # Source code
│   ├── main_test.go                  # Unit tests
│   ├── Dockerfile                    # Multi-stage Docker build
│   ├── .dockerignore
│   ├── go.mod                        # Independent Go module
│   └── go.sum
├── worker/                           # Worker microservice
│   ├── main.go                       # Source code (with Prometheus metrics)
│   ├── main_test.go                  # Unit tests
│   ├── Dockerfile                    # Multi-stage Docker build
│   ├── .dockerignore
│   ├── go.mod
│   └── go.sum
├── k8s/                              # Kubernetes manifests
│   ├── base/
│   │   ├── namespaces.yaml           # agnos-dev, agnos-uat, agnos-prod
│   │   └── network-policy.yaml       # Network policies
│   ├── envs/
│   │   ├── dev/all.yaml              # DEV: all resources in one file
│   │   ├── uat/all.yaml              # UAT: all resources in one file
│   │   └── prod/all.yaml             # PROD: all resources in one file
│   ├── chaos/                        # Chaos Mesh experiments (manual)
│   │   ├── pod-failure.yaml          # Pod kill experiment
│   │   ├── pod-stress.yaml           # CPU/memory stress
│   │   └── network-delay.yaml        # Network latency injection
│   ├── litmus/                       # LitmusChaos experiments (CI/CD)
│   │   ├── dev/                      # DEV chaos experiments
│   │   │   ├── rbac.yaml             # ServiceAccount & RBAC
│   │   │   ├── pod-delete.yaml       # Pod delete experiment
│   │   │   ├── pod-network-latency.yaml
│   │   │   └── pod-cpu-hog.yaml
│   │   └── uat/                      # UAT chaos experiments (longer durations)
│   │       ├── rbac.yaml
│   │       ├── pod-delete.yaml
│   │       ├── pod-network-latency.yaml
│   │       └── pod-cpu-hog.yaml
│   └── prometheus/
│       ├── prometheus-config.yaml
│       └── alert-rules.yaml
├── k6/                               # Load testing
│   ├── load-test.js                  # Normal load test (50 VUs)
│   └── stress-test.js                # Stress test (200 VUs)
├── .github/workflows/
│   └── ci-cd.yaml                    # 3-branch CI/CD pipeline (dev/uat/prod)
├── docker-compose.yaml               # Local dev orchestration
├── docker-compose.sonarqube.yaml     # SonarQube for code quality
├── sonar-project.properties          # SonarQube config
├── scripts/
│   └── run-local.sh                  # Run CI/CD locally with act
├── .actrc                            # act CLI config
└── .github/act.env                   # act environment variables
```

---

## CI/CD Pipeline

The CI/CD pipeline uses a **3-branch strategy** where each branch deploys exclusively to its own environment:

### Branching Strategy

```
dev ──PR──▶ uat ──PR──▶ prod
 │           │            │
 ▼           ▼            ▼
DEV only    UAT only    PROD only
```

| Branch | Trigger | Deploys To | Key Stages |
|--------|---------|-----------|------------|
| `dev` | push | DEV only | Lint, Test, SAST, Build, Scan, Deploy DEV |
| `uat` | push | UAT (pre-prod) | Lint, Test, SAST, SonarQube, Build, Scan, Deploy UAT, Chaos, Load Test |
| `prod` | push | PROD only | Lint, Test, SAST, SonarQube, Build, Scan, Deploy PROD (manual approval) |
| PR to `uat`/`prod` | pull_request | Nothing | Lint, Test, SAST (validation only) |

### Image Tagging

Images are tagged with environment + short SHA for traceability:
- `dev` branch → `agnos/api:dev-abc1234` + `agnos/api:latest`
- `uat` branch → `agnos/api:uat-abc1234` + `agnos/api:latest`
- `prod` branch → `agnos/api:prod-abc1234` + `agnos/api:latest`

### Pipeline Stages

```
                                ┌──────────────────────────────────────────────────────────────────┐
                                │                        dev branch                                │
┌───────────┐   ┌───────────┐   │  ┌───────────┐   ┌───────────┐   ┌───────────┐                   │
│ 1. Lint & │──▶│ 2. SAST   │──▶│  │ 3. Docker │──▶│ 4. Image  │──▶│ 5. Deploy │                   │
│    Test   │   │  (gosec)  │   │  │   Build   │   │   Scan    │   │    DEV    │                   │
└───────────┘   └───────────┘   │  └───────────┘   └───────────┘   └───────────┘                   │
                                └──────────────────────────────────────────────────────────────────┘
                                ┌──────────────────────────────────────────────────────────────────────────────────┐
                                │                              uat branch                                         │
                                │  ┌───────────┐   ┌───────────┐   ┌───────────┐   ┌───────────┐   ┌───────────┐  │
                                │  │ 3. Sonar- │──▶│ 4. Docker │──▶│ 5. Image  │──▶│ 6. Deploy │──▶│ 7. Chaos  │  │
                                │  │    Qube   │   │   Build   │   │   Scan    │   │    UAT    │   │  + k6     │  │
                                │  └───────────┘   └───────────┘   └───────────┘   └───────────┘   └───────────┘  │
                                └──────────────────────────────────────────────────────────────────────────────────┘
                                ┌────────────────────────────────────────────────────────────────┐
                                │                         prod branch                            │
                                │  ┌───────────┐   ┌───────────┐   ┌───────────┐   ┌───────────┐ │
                                │  │ 3. Sonar- │──▶│ 4. Docker │──▶│ 5. Image  │──▶│ 6. Deploy │ │
                                │  │    Qube   │   │   Build   │   │   Scan    │   │   PROD 🔒 │ │
                                │  └───────────┘   └───────────┘   └───────────┘   └───────────┘ │
                                └────────────────────────────────────────────────────────────────┘
```

1. **Lint & Test** — golangci-lint + `go test` with race detection and coverage (matrix: api, worker)
2. **SAST** — gosec security scan on both services
3. **SonarQube** — Code quality analysis + Quality Gate check (uat + prod only)
4. **Build** — Multi-stage Docker image builds, tagged with `ENV-SHA` (push only)
5. **Image Scan** — Trivy vulnerability and secret scanning
6. **Deploy** — Each branch deploys only to its own environment with E2E health verification
7. **Chaos Test** — LitmusChaos pod-delete experiments (uat only)
8. **Load Test** — k6 load test against Docker Compose services (uat only)

### Deployment Prerequisites

Each deploy job automatically installs the required infrastructure:
- **Kind** — Local Kubernetes cluster (1 control-plane + 2-3 workers)
- **kubectl** — Kubernetes CLI
- **CloudNativePG operator** — HA PostgreSQL via `Cluster` CRD

### Run Locally with act

```bash
# Prerequisites: act, Docker (recommended: 4+ GB RAM, 4+ CPUs in Docker Desktop)
brew install act

# Run full pipeline (default: uses current branch)
./scripts/run-local.sh

# Run a specific job
./scripts/run-local.sh lint-and-test
./scripts/run-local.sh build
./scripts/run-local.sh deploy-dev
```

### Run act for Each Environment

Each branch triggers a different pipeline. Use event files to simulate pushes to specific branches:

```bash
# DEV pipeline: lint → sast → build → image-scan → deploy-dev
act push --eventpath .github/events/push-dev.json --env-file .github/act.env --privileged

# UAT pipeline: lint → sast → sonarqube → build → image-scan → deploy-uat → chaos-test + load-test
act push --eventpath .github/events/push-uat.json --env-file .github/act.env --privileged

# PROD pipeline: lint → sast → sonarqube → build → image-scan → deploy-prod
act push --eventpath .github/events/push-prod.json --env-file .github/act.env --privileged

# Run a specific job only
act push --eventpath .github/events/push-dev.json --env-file .github/act.env --privileged -j deploy-dev
```

**Note:** SonarQube must be running for UAT/PROD pipelines:
```bash
docker compose -f docker-compose.sonarqube.yaml up -d
```

### Deploy All Environments Manually (without act)

```bash
# 1. Build images
docker build -t agnos/api:latest ./api
docker build -t agnos/worker:latest ./worker

# 2. Create Kind cluster
kind create cluster --name agnos-cluster --config=- <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: 30090
        hostPort: 9090
      - containerPort: 30091
        hostPort: 9091
      - containerPort: 30092
        hostPort: 9092
  - role: worker
  - role: worker
  - role: worker
EOF

# 3. Load images and install CNPG
kind load docker-image agnos/api:latest agnos/worker:latest --name agnos-cluster
kubectl apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.25/releases/cnpg-1.25.1.yaml
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=cloudnative-pg -n cnpg-system --timeout=120s

# 4. Deploy all environments
kubectl apply -f k8s/base/namespaces.yaml
kubectl apply -f k8s/envs/dev/all.yaml
kubectl apply -f k8s/envs/uat/all.yaml
kubectl apply -f k8s/envs/prod/all.yaml

# 5. Wait for pods
kubectl get pods -A -w
```

---

## Local Development

### Run with Docker Compose

```bash
docker compose up --build
```

This starts PostgreSQL, API (port 8080), and Worker (port 8081) with health checks.

### Run Services Directly

```bash
# API (terminal 1)
cd api && APP_ENV=development DB_DSN="postgres://agnos_dev:agnos_dev_pass@localhost:5433/agnos_dev?sslmode=disable" go run .

# Worker (terminal 2)
cd worker && APP_ENV=development WORKER_INTERVAL=10s DB_DSN="postgres://agnos_dev:agnos_dev_pass@localhost:5433/agnos_dev?sslmode=disable" go run .
```

### Accessing the Local Database

**1. If running with Docker Compose:**
The database is mapped to host port `5433` to prevent conflicts with local instances.
- **URI**: `postgres://agnos_dev:agnos_dev_pass@localhost:5433/agnos_dev?sslmode=disable`
- **Host**: `localhost` | **Port**: `5433` | **DB**: `agnos_dev` | **User**: `agnos_dev` | **Password**: `agnos_dev_pass`

**2. If running via `act` (Kind cluster):**
The Kind cluster remains active when using `.github/act.env` (`SKIP_KIND_CLEANUP=true`). You can port-forward the CloudNativePG `postgres-rw` service to your host:

```bash
# Port-forward the DEV database to local port 5432
kubectl port-forward -n agnos-dev svc/postgres-rw 5432:5432
```
After port-forwarding, connect via:
- **URI**: `postgres://agnos_dev:agnos_dev_pass@localhost:5432/agnos_dev?sslmode=disable`
- **Host**: `localhost` | **Port**: `5432` | **DB**: `agnos_dev` | **User**: `agnos_dev` | **Password**: `agnos_dev_pass`


### Verify

```bash
# Liveness
curl http://localhost:8080/live

# Readiness
curl http://localhost:8080/ready

# Prometheus metrics (API)
curl http://localhost:8080/metrics

# Prometheus metrics (Worker)
curl http://localhost:8081/metrics
```

### Public API

The API exposes a dedicated public endpoint on a separate port (`PUBLIC_PORT`, default `8090`). This endpoint is the **only** externally accessible route via NodePort; internal endpoints (`/live`, `/ready`, `/metrics`) remain cluster-internal on port 8080.

```bash
# Public time endpoint
curl http://localhost:8090/api/v1/time
# {"status":"ok","timestamp":"2026-02-27T12:00:00Z","env":"development"}
```

**Per-environment NodePort access (Kind cluster):**

| Env  | NodePort | Host Port | URL |
|------|----------|-----------|-----|
| DEV  | 30090    | 9090      | `http://localhost:9090/api/v1/time` |
| UAT  | 30091    | 9091      | `http://localhost:9091/api/v1/time` |
| PROD | 30092    | 9092      | `http://localhost:9092/api/v1/time` |

**Network policy design:**

```
External traffic --> NodePort 300xx --> port 8090 --> /api/v1/time ONLY (public server)
Cluster internal --> ClusterIP :80  --> port 8080 --> /live, /ready, /metrics (internal server)
```

---

## How to Run Tests

```bash
# API tests
cd api && go test -v -race ./...

# Worker tests
cd worker && go test -v -race ./...

# With coverage
cd api && go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

# Automated Test Result Collection
# Gather unit tests, SAST reports, SonarQube metrics, and load/stress test
# output into the `results/` folder (runs locally or leverages act CI cache)
./scripts/collect-results.sh
```

---

## Load Testing

```bash
# Install k6
brew install k6

# Normal load test (50 VUs, 5 min)
k6 run k6/load-test.js

# Stress test (200 VUs, 8 min)
k6 run k6/stress-test.js

# Against a specific endpoint
k6 run --env BASE_URL=http://api-uat.agnos.local k6/load-test.js
```

---

## Docker

### Build Images

```bash
docker build -t agnos/api:latest ./api
docker build -t agnos/worker:latest ./worker
```

### Multi-stage Build

Both Dockerfiles use a multi-stage build:
1. **Builder** (`golang:1.25-alpine`): Compiles with `-ldflags="-w -s"` for minimal binary
2. **Runtime** (`gcr.io/distroless/static:nonroot`): Minimal attack surface, non-root user

### Environment Variables

| Variable | Default | Used By | Description |
|----------|---------|---------|-------------|
| `APP_ENV` | `development` | Both | Environment name |
| `APP_VERSION` | `1.0.0` | API | Version in health response |
| `PORT` | `8080` | API | Internal API listen port |
| `PUBLIC_PORT` | `8090` | API | Public API listen port |
| `HEALTH_PORT` | `8081` | Worker | Worker health port |
| `WORKER_INTERVAL` | `60s` | Worker | Job interval |
| `DB_DSN` | — | Both | PostgreSQL connection string |
| `RATE_LIMIT` | `100` | API | Requests per second limit |

---

## Kubernetes

### Deploy to Kind

Each branch deploys only its own environment to a Kind cluster. The CI/CD pipeline automatically installs Kind, kubectl, and the CloudNativePG operator before deploying.

UAT is configured as a **pre-prod** environment — identical resource/replica/HA config as PROD so that chaos tests and load tests accurately simulate production behavior.

| Environment | Namespace | API Replicas | Worker Replicas | Postgres Instances (CNPG) | Notes |
|-------------|-----------|-------------|----------------|--------------------------|-------|
| DEV | `agnos-dev` | 1 | 1 | 2 (1 primary + 1 replica) | Lightweight for fast iteration |
| UAT (pre-prod) | `agnos-uat` | 3 | 2 | 3 (1 primary + 2 replicas) | Mirrors PROD config |
| PROD | `agnos-prod` | 3 | 2 | 3 (1 primary + 2 replicas) | Manual approval required |

### Manual Deploy

```bash
# Install CloudNativePG operator first (required for PostgreSQL HA clusters)
kubectl apply --server-side -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.25/releases/cnpg-1.25.1.yaml
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=cloudnative-pg -n cnpg-system --timeout=120s

# Create namespaces and deploy environments
kubectl apply -f k8s/base/namespaces.yaml
kubectl apply -f k8s/envs/dev/all.yaml
kubectl apply -f k8s/envs/uat/all.yaml
kubectl apply -f k8s/envs/prod/all.yaml
```

### Connect to Kind Cluster (kubectl / Lens)

After the CI/CD pipeline runs with `SKIP_KIND_CLEANUP=true`, the Kind cluster remains available. To connect:

```bash
# List available Kind clusters
kind get clusters

# Set kubectl context to the Kind cluster
kubectl cluster-info --context kind-agnos-cluster

# If the context is not set automatically, export the kubeconfig
kind export kubeconfig --name agnos-cluster

# Verify connection
kubectl get nodes
kubectl get pods -A

# View resources per environment
kubectl get all -n agnos-dev
kubectl get all -n agnos-uat
kubectl get all -n agnos-prod
```

To use with **Lens**:
1. Run `kind export kubeconfig --name agnos-cluster` to ensure the context is in `~/.kube/config`
2. Open Lens and it will auto-detect the `kind-agnos-cluster` context
3. Or manually add the cluster via File > Add Cluster and paste the output of `kind get kubeconfig --name agnos-cluster`

### Clean Up Kind Cluster

```bash
# Delete the Kind cluster (removes all deployed environments)
kind delete cluster --name agnos-cluster

# If using a separate chaos testing cluster
kind delete cluster --name agnos-chaos

# Delete all Kind clusters
kind delete clusters --all
```

### CloudNativePG (HA PostgreSQL)

Each environment uses a CloudNativePG `Cluster` CRD instead of a single Postgres Deployment:
- **Streaming replication** with automatic failover
- **Self-healing**: failed instances are automatically replaced
- **`postgres-rw`** service: always points to the primary (read-write)
- **`postgres-ro`** service: load-balanced across replicas (read-only)
- **PodMonitor** for Prometheus metrics

### High Availability Features

- **Multiple replicas** with pod anti-affinity across nodes
- **HPA**: Auto-scaling based on CPU (70%) / memory (80%)
- **PodDisruptionBudget**: Minimum availability during disruptions
- **Rolling updates**: `maxUnavailable: 0`, `maxSurge: 1`
- **Readiness/liveness probes** on both services
- **Resource requests/limits** to prevent noisy neighbors
- **ResourceQuota / LimitRange** per namespace
- **NetworkPolicy** for all components (API, Worker, Postgres)
- **Ingress** for UAT/PROD external access
- **Graceful shutdown** period

### Security Notes

Secrets in `k8s/envs/*/all.yaml` contain **placeholder values** for local/CI usage only. In a real production environment, use one of:
- [Sealed Secrets](https://github.com/bitnami-labs/sealed-secrets)
- [External Secrets Operator](https://external-secrets.io/)
- [SOPS](https://github.com/getsops/sops) with age/KMS encryption

---

## Monitoring

### Structured JSON Logs

```json
{"time":"2026-02-25T14:00:00Z","level":"INFO","msg":"request completed","method":"GET","path":"/live","status":200,"duration_ms":0.5}
```

### Prometheus Metrics

**API Metrics:**

| Metric | Type | Description |
|--------|------|-------------|
| `http_requests_total` | Counter | Requests by method/endpoint/status |
| `http_request_duration_seconds` | Histogram | Latency distribution |
| `http_errors_total` | Counter | 4xx/5xx errors |
| `http_rate_limited_total` | Counter | Rate-limited requests |

**Worker Metrics:**

| Metric | Type | Description |
|--------|------|-------------|
| `worker_logs_processed_total` | Counter | Total log entries processed |
| `worker_processing_duration_seconds` | Histogram | Batch processing duration |
| `worker_batch_errors_total` | Counter | Batch processing errors |

### Alert Rules (`k8s/prometheus/alert-rules.yaml`)

| Alert | Condition | Severity |
|-------|-----------|----------|
| HighErrorRate | >5% errors for 5 min | Critical |
| HighRequestLatency | p95 >1s for 5 min | Warning |
| WorkerStalled | Unhealthy for 5 min | Critical |
| PodCrashLooping | Frequent restarts | Critical |
| PodNotReady | Not ready for 5 min | Warning |
| HighDBLatency | DB p95 >500ms for 5 min | Warning |
| HighMemoryUsage | >80% memory limit for 5 min | Warning |
| APIDown | All replicas down for 1 min | Critical |

---

## Chaos Engineering

### LitmusChaos (CI/CD Integrated)

LitmusChaos experiments in `k8s/litmus/` run automatically in the CI/CD pipeline for UAT:

| Experiment | UAT | Description |
|-----------|-----|-------------|
| `pod-delete` | 60s, 50% pods | Random pod kill to validate auto-recovery |
| `pod-network-latency` | 120s, 300ms | Network latency injection |
| `pod-cpu-hog` | 120s, 1 core | CPU stress to validate HPA scaling |

```bash
# Manual usage
kubectl apply -f k8s/litmus/uat/rbac.yaml
kubectl apply -f k8s/litmus/uat/pod-delete.yaml
kubectl get chaosresult -n agnos-uat
```

### Chaos Mesh (Manual)

Additional Chaos Mesh experiments in `k8s/chaos/` for ad-hoc resilience testing:

| Experiment | Target | Description |
|-----------|--------|-------------|
| `pod-failure.yaml` | API pods | Random pod kill every 10 min |
| `pod-stress.yaml` | API pods | CPU (80%) + memory (256MB) stress for 5 min |
| `network-delay.yaml` | API → Postgres | 200ms latency injection for 5 min |

---

## Failure Scenario Handling

### 1. API crashes during peak hours

**Detection:** Liveness probe fails → K8s restarts pod. HPA detects high CPU → scales up.

**Mitigation:** Multiple replicas + anti-affinity ensure availability. HPA auto-scales for load spikes. HighErrorRate alert notifies team.

**Recovery:** K8s auto-restarts crashed pods. Load balancer routes to healthy pods. Investigate via structured logs.

### 2. Worker fails and infinitely retries

**Detection:** Health endpoint returns 503 → liveness probe fails → K8s restarts.

**Mitigation:** Staleness detection (unhealthy if no job runs within 3x interval). WorkerStalled alert fires after 5 min. Resource limits prevent runaway consumption.

**Recovery:** K8s restarts after liveness failure. Add exponential backoff + max retry limit + dead-letter queue for persistent issues.

### 3. Bad deployment is released

**Detection:** Readiness probe fails on new pods → no traffic routed.

**Mitigation:** Rolling update with `maxUnavailable: 0` keeps old pods serving. CI/CD runs lint, tests, SAST, and Trivy before deploy. Code must pass through `dev` → `uat` → `prod` branches via PRs. PROD requires manual approval via GitHub Environment protection rules.

**Recovery:**
```bash
kubectl rollout undo deployment/api -n agnos-prod
```

### 4. Kubernetes node goes down

**Detection:** K8s marks pods as Terminating.

**Mitigation:** Pod anti-affinity spreads replicas across nodes. ReplicaSet reschedules to healthy nodes. PodDisruptionBudget ensures minimum availability.

**Recovery:** Automatic — K8s reschedules pods. No manual intervention for stateless services.

### 5. PostgreSQL primary fails

**Detection:** CloudNativePG detects primary failure via health checks.

**Mitigation:** CloudNativePG automatically promotes a replica to primary. `postgres-rw` service seamlessly points to the new primary.

**Recovery:** Automatic — CNPG self-heals by recreating the failed instance as a new replica.

---

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.25 |
| Database | PostgreSQL 15 (CloudNativePG HA) |
| Container | Docker (multi-stage, distroless) |
| Orchestration | Kubernetes (Kind) |
| CI/CD | GitHub Actions (3-branch strategy: dev/uat/prod) |
| Linting | golangci-lint |
| SAST | gosec |
| Code Quality | SonarQube + Quality Gate |
| Image Scan | Trivy |
| Load Testing | k6 |
| Chaos Engineering | LitmusChaos (CI/CD) + Chaos Mesh (manual) |
| Monitoring | Prometheus |
| Logging | `log/slog` (structured JSON) |
