# Tiny Systems Distribution Module

Container image distribution components for registry automation workflows.

## Components

| Component | Description |
|-----------|-------------|
| Registry Copy | Copy a container image between registries (push, mirror, promote) |
| Registry Catalog | List all repositories and tags in a registry |

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
