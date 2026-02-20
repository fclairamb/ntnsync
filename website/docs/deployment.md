---
sidebar_position: 6
---

# Deployment

This guide covers deploying ntnsync on Kubernetes for continuous synchronization.

## Kubernetes Deployment

### Prerequisites

- A Kubernetes cluster with an ingress controller (e.g., nginx, Traefik)
- cert-manager for TLS certificates (optional)
- A Notion API token
- A remote git repository for pushing synced content

### Manifests

Create a file `ntnsync.yaml` with the following content:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ntnsync
  namespace: dev
type: Opaque
stringData:
  NOTION_TOKEN: "secret_xxx"       # Replace with your Notion token
  NTN_GIT_PASS: "ghp_xxx"         # Replace with your Git token
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ntnsync
  namespace: dev
  labels:
    app.kubernetes.io/name: ntnsync
    app.kubernetes.io/instance: ntnsync
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: ntnsync
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ntnsync
        app.kubernetes.io/instance: ntnsync
    spec:
      containers:
      - name: ntnsync
        image: ghcr.io/fclairamb/ntnsync:0.6.3
        args: ["serve", "--verbose"]
        ports:
        - containerPort: 8080
        env:
        - name: NTN_GIT_URL
          value: "https://github.com/your-org/your-repo.git"
        - name: NTN_DIR
          value: "/tmp/data"
        - name: NTN_COMMIT
          value: "true"
        - name: NTN_COMMIT_PERIOD
          value: "1m"
        - name: NTN_LOG_FORMAT
          value: "json"
        - name: NTN_BLOCK_DEPTH
          value: "5"
        - name: NTN_GIT_PASS
          valueFrom:
            secretKeyRef:
              name: ntnsync
              key: NTN_GIT_PASS
        - name: NOTION_TOKEN
          valueFrom:
            secretKeyRef:
              name: ntnsync
              key: NOTION_TOKEN
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          periodSeconds: 10
        startupProbe:
          httpGet:
            path: /health
            port: 8080
          failureThreshold: 180
          periodSeconds: 10
        resources:
          limits:
            cpu: "3"
            memory: 512Mi
          requests:
            memory: 20Mi
---
apiVersion: v1
kind: Service
metadata:
  name: ntnsync
  namespace: dev
  labels:
    app.kubernetes.io/name: ntnsync
    app.kubernetes.io/instance: ntnsync
spec:
  type: ClusterIP
  ports:
  - port: 80
    protocol: TCP
    targetPort: 8080
  selector:
    app.kubernetes.io/name: ntnsync
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ntnsync
  namespace: dev
  labels:
    app.kubernetes.io/name: ntnsync
    app.kubernetes.io/instance: ntnsync
spec:
  ingressClassName: nginx
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

### Key design choices

**Remote git push instead of persistent volumes**: With `NTN_GIT_URL` and `NTN_DIR=/tmp/data`, ntnsync clones the repo on startup and pushes changes back. This avoids the need for a PersistentVolumeClaim and makes the pod stateless — if it restarts, it simply re-clones and continues.

**Startup probe with high threshold**: The initial clone and sync can take time. The startup probe allows up to 30 minutes (`failureThreshold: 180 * periodSeconds: 10`) before considering the pod failed.

**Periodic commits**: `NTN_COMMIT_PERIOD=1m` commits changes every minute during long sync operations, preventing data loss if the pod is evicted.

**Block depth limit**: `NTN_BLOCK_DEPTH=5` limits nested block fetching to 5 levels, speeding up syncs for deeply nested pages.

### Applying the Manifests

```bash
# Create the secret separately (recommended)
kubectl create secret generic ntnsync \
  --namespace dev \
  --from-literal=NOTION_TOKEN=secret_xxx \
  --from-literal=NTN_GIT_PASS=ghp_xxx

# Apply the deployment
kubectl apply -f ntnsync.yaml
```

### Configuration

Configure ntnsync using environment variables:

| Variable | Description |
|----------|-------------|
| `NOTION_TOKEN` | Notion integration token (required) |
| `NTN_GIT_URL` | Remote git repository URL |
| `NTN_GIT_PASS` | Git password/token for authentication |
| `NTN_DIR` | Storage directory (default: `notion`, use `/tmp/data` for ephemeral storage) |
| `NTN_COMMIT` | Set to `true` to enable automatic git commits |
| `NTN_COMMIT_PERIOD` | Commit periodically during sync (e.g., `1m`, `5m`) |
| `NTN_LOG_FORMAT` | Log format: `text` or `json` (use `json` for log aggregation) |
| `NTN_BLOCK_DEPTH` | Max block discovery depth (0 = unlimited, 5 is a good default) |
| `NTN_WEBHOOK_SECRET` | Webhook secret for HMAC signature verification |
| `NTN_WEBHOOK_AUTO_SYNC` | Auto-sync after receiving events (default: `true`) |
| `NTN_WEBHOOK_SYNC_DELAY` | Debounce delay before processing (e.g., `5s`) |

### Alternative: Persistent Volume

If you prefer local storage over remote git push, add a PersistentVolumeClaim and set `NTN_DIR=/data`:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ntnsync-data
  namespace: dev
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
        env:
        - name: NTN_DIR
          value: "/data"
```

### Verifying the Deployment

```bash
# Check pod status
kubectl get pods -n dev -l app.kubernetes.io/name=ntnsync

# View logs
kubectl logs -n dev -l app.kubernetes.io/name=ntnsync

# Test the health endpoint
kubectl port-forward -n dev svc/ntnsync 8080:80
curl http://localhost:8080/health
```

### Configuring Notion Webhooks

Once deployed, configure your Notion integration to send webhooks:

1. Go to your [Notion integrations page](https://www.notion.so/my-integrations)
2. Select your integration
3. Enable webhooks and set the URL to `https://ntnsync.example.com/webhooks/notion`
4. Copy the webhook signing secret and add it to your Kubernetes secret:

```bash
kubectl create secret generic ntnsync \
  --namespace dev \
  --from-literal=NOTION_TOKEN=secret_xxx \
  --from-literal=NTN_GIT_PASS=ghp_xxx \
  --from-literal=NTN_WEBHOOK_SECRET=your-secret \
  --dry-run=client -o yaml | kubectl apply -f -
```

The webhook server will:
- Receive page update events from Notion
- Verify the webhook signature (when `NTN_WEBHOOK_SECRET` is set)
- Queue changed pages for sync
- Automatically process the queue and commit/push changes
