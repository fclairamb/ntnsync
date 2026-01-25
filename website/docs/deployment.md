---
sidebar_position: 6
---

# Deployment

This guide covers deploying ntnsync on Kubernetes for continuous synchronization.

## Kubernetes Deployment

### Prerequisites

- A Kubernetes cluster with an ingress controller (e.g., Traefik)
- cert-manager for TLS certificates (optional)
- A Notion API token

### Manifests

Create a file `ntnsync.yaml` with the following content:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: ntnsync
---
apiVersion: v1
kind: Secret
metadata:
  name: ntnsync
  namespace: ntnsync
type: Opaque
stringData:
  NOTION_TOKEN: "secret_xxx"  # Replace with your token
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ntnsync
  namespace: ntnsync
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ntnsync
  template:
    metadata:
      labels:
        app: ntnsync
    spec:
      containers:
      - name: ntnsync
        image: ghcr.io/fclairamb/ntnsync:latest
        ports:
        - containerPort: 80
        envFrom:
        - secretRef:
            name: ntnsync
        resources:
          limits:
            cpu: 100m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 32Mi
---
apiVersion: v1
kind: Service
metadata:
  name: ntnsync
  namespace: ntnsync
spec:
  type: ClusterIP
  ports:
  - name: http
    port: 80
    targetPort: 80
  selector:
    app: ntnsync
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ntnsync
  namespace: ntnsync
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  ingressClassName: traefik
  rules:
  - host: ntnsync.example.com  # Replace with your domain
    http:
      paths:
      - backend:
          service:
            name: ntnsync
            port:
              number: 80
        path: /
        pathType: Prefix
  tls:
  - hosts:
    - ntnsync.example.com  # Replace with your domain
    secretName: ntnsync-tls
```

### Applying the Manifests

```bash
# Create the secret separately (recommended)
kubectl create namespace ntnsync
kubectl create secret generic ntnsync \
  --namespace ntnsync \
  --from-literal=NOTION_TOKEN=secret_xxx

# Apply the deployment
kubectl apply -f ntnsync.yaml
```

### Configuration

Configure ntnsync using environment variables in the Secret:

| Variable | Description |
|----------|-------------|
| `NOTION_TOKEN` | Notion integration token (required) |
| `NTN_DIR` | Storage directory (default: `notion`) |
| `NTN_COMMIT` | Set to `true` to enable automatic git commits |
| `NTN_PUSH` | Set to `true` to push to remote |
| `NTN_GIT_URL` | Remote git repository URL |
| `NTN_GIT_PASS` | Git password/token for authentication |

### Persistent Storage

For persistent storage of synced files, add a PersistentVolumeClaim:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ntnsync-data
  namespace: ntnsync
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
---
# Add to the Deployment spec.template.spec:
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: ntnsync-data
      containers:
      - name: ntnsync
        volumeMounts:
        - name: data
          mountPath: /data
```

### Verifying the Deployment

```bash
# Check pod status
kubectl get pods -n ntnsync

# View logs
kubectl logs -n ntnsync -l app=ntnsync

# Test the service
kubectl port-forward -n ntnsync svc/ntnsync 8080:80
```
