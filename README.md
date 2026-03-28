# Tiny Systems Distribution Module

Container image distribution for edge and air-gapped Kubernetes environments.

## Components

| Component | Description |
|-----------|-------------|
| Registry Catalog | List repositories and tags from any OCI-compliant registry |
| Registry Copy | Copy container images between registries |

## How it works

Use these components with a 3rd-party OCI registry (Zot, Harbor, distribution/registry, etc.) installed via its own Helm chart. This module handles image discovery and replication — the registry is external infrastructure.

## Installation

```shell
helm repo add tinysystems https://tiny-systems.github.io/module/
helm install distribution-module tinysystems/tinysystems-operator \
  --set controllerManager.manager.image.repository=ghcr.io/tiny-systems/distribution-module
```

## Run locally

```shell
go run cmd/main.go run --name=distribution-module --namespace=tinysystems --version=1.0.0
```

## Part of Tiny Systems

This module is part of the [Tiny Systems](https://github.com/tiny-systems) platform -- a visual flow-based automation engine running on Kubernetes.

## License

This module's source code is MIT-licensed. It depends on the [Tiny Systems Module SDK](https://github.com/tiny-systems/module) (BSL 1.1). See [LICENSE](LICENSE) for details.
