# LitmusChaos Experiments

Resilience testing using [LitmusChaos](https://litmuschaos.io/) for DEV and UAT environments.

## Prerequisites

Install LitmusChaos operator on your cluster:

```bash
kubectl apply -f https://litmuschaos.github.io/litmus/litmus-operator-v3.0.0.yaml
```

## Experiments

### Pod Delete
Randomly kills API pods to validate auto-recovery and zero-downtime resilience.
- **DEV**: 30s duration, 10s interval, 50% pods affected
- **UAT**: 60s duration, 15s interval, 50% pods affected

### Pod Network Latency
Injects network latency into API pods to validate timeout handling and degraded-mode behavior.
- **DEV**: 60s duration, 200ms latency, 50ms jitter
- **UAT**: 120s duration, 300ms latency, 100ms jitter

### Pod CPU Hog
Stresses CPU on API pods to validate HPA scaling and resource limit enforcement.
- **DEV**: 60s duration, 1 CPU core, 50% pods affected
- **UAT**: 120s duration, 1 CPU core, 50% pods affected

## Usage

```bash
# Apply RBAC first
kubectl apply -f k8s/litmus/dev/rbac.yaml
kubectl apply -f k8s/litmus/uat/rbac.yaml

# Run a specific experiment
kubectl apply -f k8s/litmus/dev/pod-delete.yaml
kubectl apply -f k8s/litmus/uat/pod-network-latency.yaml

# Check experiment status
kubectl get chaosresult -n agnos-dev
kubectl get chaosresult -n agnos-uat

# Cleanup
kubectl delete -f k8s/litmus/dev/
kubectl delete -f k8s/litmus/uat/
```

## CI/CD Integration

The `chaos-test` job in the CI/CD pipeline:
1. Installs the LitmusChaos operator on the Kind cluster
2. Applies RBAC and pod-delete experiments for DEV and UAT
3. Waits for chaos experiments to complete
4. Verifies services recovered (E2E health checks)
5. Checks ChaosResult verdict (Pass/Fail)
