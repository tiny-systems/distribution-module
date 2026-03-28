package registrycopy

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/module"
	"github.com/tiny-systems/module/registry"
)

const (
	ComponentName = "registry_copy"
	RequestPort   = "request"
	ResultPort    = "result"
	ErrorPort     = "error"
)

type Context any

type Settings struct {
	EnableErrorPort bool `json:"enableErrorPort" title:"Enable Error Port" description:"Output errors to error port instead of failing"`
}

type Request struct {
	Context  Context `json:"context,omitempty" configurable:"true" title:"Context" description:"Arbitrary context to pass through"`
	Source   string  `json:"source" required:"true" title:"Source" description:"Source image reference (e.g. docker.io/library/nginx:latest)"`
	Target   string  `json:"target" required:"true" title:"Target" description:"Target image reference (e.g. localhost:5000/nginx:latest)"`
	Insecure bool    `json:"insecure,omitempty" title:"Insecure" description:"Allow insecure (HTTP) connections. Required when target is a localhost registry."`
}

type CopyResult struct {
	Source string `json:"source" title:"Source"`
	Target string `json:"target" title:"Target"`
	Digest string `json:"digest" title:"Digest" description:"Image digest after copy"`
}

type Result struct {
	Context Context    `json:"context,omitempty" title:"Context"`
	Result  CopyResult `json:"result" title:"Result"`
}

type Error struct {
	Context Context `json:"context,omitempty" title:"Context"`
	Error   string  `json:"error" title:"Error"`
}

type Component struct {
	settings     Settings
	settingsLock sync.RWMutex
}

func (c *Component) Instance() module.Component {
	return &Component{}
}

func (c *Component) GetInfo() module.ComponentInfo {
	return module.ComponentInfo{
		Name:        ComponentName,
		Description: "Registry Copy",
		Info:        "Copy a container image from one registry to another. Copies all layers and manifests. Use for image replication, mirroring, or edge distribution. Set insecure=true when the target is a localhost registry (same pod).",
		Tags:        []string{"OCI", "Registry", "Container", "Copy", "Replicate"},
	}
}

func (c *Component) Handle(ctx context.Context, handler module.Handler, port string, msg any) any {
	switch port {
	case v1alpha1.SettingsPort:
		in, ok := msg.(Settings)
		if !ok {
			return fmt.Errorf("invalid settings")
		}
		c.settingsLock.Lock()
		c.settings = in
		c.settingsLock.Unlock()
		return nil

	case RequestPort:
		in, ok := msg.(Request)
		if !ok {
			return fmt.Errorf("invalid request")
		}
		return c.handleRequest(ctx, handler, in)
	}

	return fmt.Errorf("unknown port: %s", port)
}

func (c *Component) handleRequest(ctx context.Context, handler module.Handler, req Request) any {
	if req.Source == "" || req.Target == "" {
		return c.handleError(ctx, handler, req, "source and target are required")
	}

	opts := []crane.Option{crane.WithContext(ctx)}
	if req.Insecure {
		opts = append(opts, crane.Insecure)
	}

	if err := crane.Copy(req.Source, req.Target, opts...); err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("copy failed: %v", err))
	}

	digest, err := crane.Digest(req.Target, opts...)
	if err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("copy succeeded but failed to get digest: %v", err))
	}

	return handler(ctx, ResultPort, Result{
		Context: req.Context,
		Result: CopyResult{
			Source: req.Source,
			Target: req.Target,
			Digest: digest,
		},
	})
}

func (c *Component) handleError(ctx context.Context, handler module.Handler, req Request, errMsg string) any {
	c.settingsLock.RLock()
	enableErrorPort := c.settings.EnableErrorPort
	c.settingsLock.RUnlock()

	if enableErrorPort {
		return handler(ctx, ErrorPort, Error{
			Context: req.Context,
			Error:   errMsg,
		})
	}
	return errors.New(errMsg)
}

func (c *Component) Ports() []module.Port {
	c.settingsLock.RLock()
	enableErrorPort := c.settings.EnableErrorPort
	c.settingsLock.RUnlock()

	ports := []module.Port{
		{
			Name:          v1alpha1.SettingsPort,
			Label:         "Settings",
			Configuration: Settings{},
		},
		{
			Name:  RequestPort,
			Label: "Request",
			Configuration: Request{
				Source: "docker.io/library/nginx:latest",
				Target: "localhost:5000/nginx:latest",
			},
			Position: module.Left,
		},
		{
			Name:   ResultPort,
			Label:  "Result",
			Source: true,
			Configuration: Result{
				Result: CopyResult{
					Source: "docker.io/library/nginx:latest",
					Target: "localhost:5000/nginx:latest",
					Digest: "sha256:abc123...",
				},
			},
			Position: module.Right,
		},
	}

	if enableErrorPort {
		ports = append(ports, module.Port{
			Name:          ErrorPort,
			Label:         "Error",
			Source:        true,
			Configuration: Error{},
			Position:      module.Bottom,
		})
	}

	return ports
}

var _ module.Component = (*Component)(nil)

func init() {
	registry.Register(&Component{})
}
