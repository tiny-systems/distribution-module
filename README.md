# Tiny Systems Distribution Module

Container image distribution for edge and air-gapped Kubernetes environments.

## Components

| Component | Description |
|-----------|-------------|
| Container Registry | OCI-compliant registry server with standalone and pull-through cache modes |

## How it works

**Standalone mode** — a local registry that accepts push and pull. Configure a port, start it, push images.

**Pull-through cache mode** — set an upstream registry host (e.g., `registry-1.docker.io`). When a pod pulls an image, the registry fetches it from upstream and caches it locally. Subsequent pulls are served from cache. No deployment changes needed — just configure containerd to mirror through this registry.

## Installation

```shell
helm repo add tinysystems https://tiny-systems.github.io/module/
helm install distribution-module tinysystems/tinysystems-operator \
  --set controllerManager.manager.image.repository=ghcr.io/tiny-systems/distribution-module \
  --set storage.enabled=true \
  --set storage.size=10Gi
```

## Run locally

```shell
go run cmd/main.go run --name=distribution-module --namespace=tinysystems --version=1.0.0
```

## Part of Tiny Systems

This module is part of the [Tiny Systems](https://github.com/tiny-systems) platform -- a visual flow-based automation engine running on Kubernetes.

## License

This module's source code is MIT-licensed. It depends on the [Tiny Systems Module SDK](https://github.com/tiny-systems/module) (BSL 1.1). See [LICENSE](LICENSE) for details.
