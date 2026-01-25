#!/bin/sh
set -ex

echo "==> Checking if local server is running on port 8080..."
if ! lsof -i :8080 >/dev/null 2>&1; then
  echo "ERROR: No service listening on port 8080"
  echo "Start your local webhook server first"
  exit 1
fi
echo "✓ Local server is running"

echo "==> Connecting to Kubernetes cluster..."
telepresence connect --namespace ntnsync-dev

echo "==> Cleaning up any existing intercept..."
telepresence leave ntnsync 2>/dev/null || echo "  (no existing intercept)"

echo "==> Creating intercept: ntnsync 8080:80..."
telepresence intercept ntnsync --port 8080:80

echo ""
echo "==> Intercept is active!"
echo "Traffic from https://ntnsync.${TEST_DOMAIN} is now routed to localhost:8080"
echo ""

echo "==> Testing intercept..."
if curl -sf https://ntnsync.${TEST_DOMAIN}/health >/dev/null 2>&1; then
  echo "✓ Intercept is working! Response from https://ntnsync.${TEST_DOMAIN}:"
  curl -s https://ntnsync.${TEST_DOMAIN}/health
  echo ""
else
  echo "⚠ Warning: Health check failed. The intercept may not be working correctly."
  echo "Check your local server logs for errors."
fi

echo ""
echo "To stop the intercept, run:"
echo "  telepresence leave ntnsync"
