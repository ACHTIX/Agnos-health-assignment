# Chaos Engineering Experiments

These experiments use [Chaos Mesh](https://chaos-mesh.org/) to validate system resilience.

## Prerequisites

Install Chaos Mesh on your cluster:

```bash
kubectl create ns chaos-mesh
helm repo add chaos-mesh https://charts.chaos-mesh.org
helm install chaos-mesh chaos-mesh/chaos-mesh -n chaos-mesh --set chaosDaemon.runtime=containerd
```

## Experiments

### Pod Failure (`pod-failure.yaml`)
Kills a random API pod every 10 minutes. Validates that:
- Kubernetes restarts the pod automatically
- Other replicas continue serving traffic
- PDB prevents killing too many pods at once

```bash
kubectl apply -f pod-failure.yaml
```

### Network Delay (`network-delay.yaml`)
Injects 200ms latency (with 50ms jitter) between API pods and Postgres for 5 minutes. Validates that:
- API continues to respond (with degraded latency)
- No cascading failures occur
- Retry logic handles transient timeouts

```bash
kubectl apply -f network-delay.yaml
```

### Pod Stress (`pod-stress.yaml`)
Applies CPU (80% on 2 workers) and memory (256MB) stress to one API pod for 5 minutes. Validates that:
- HPA scales up additional replicas
- Resource limits prevent noisy-neighbor effects
- Alerts fire for high resource usage

```bash
kubectl apply -f pod-stress.yaml
```

## Cleanup

Remove all experiments:

```bash
kubectl delete -f k8s/chaos/
```
