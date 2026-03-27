package registrycatalog

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
	ComponentName = "registry_catalog"
	RequestPort   = "request"
	ResultPort    = "result"
	ErrorPort     = "error"
)

// Context type alias for schema generation
type Context any

// Settings configures the component
type Settings struct {
	EnableErrorPort bool `json:"enableErrorPort" title:"Enable Error Port" description:"Output errors to error port instead of failing"`
}

// Request is the input to list a registry's catalog
type Request struct {
	Context  Context `json:"context,omitempty" configurable:"true" title:"Context" description:"Arbitrary context to pass through"`
	Registry string  `json:"registry" required:"true" title:"Registry" description:"Registry hostname (e.g., docker.io, ghcr.io, registry.local:5000)"`
	Insecure bool    `json:"insecure,omitempty" title:"Insecure" description:"Allow insecure (HTTP) registry connections"`
}

// RepoInfo contains repository details
type RepoInfo struct {
	Name string   `json:"name" title:"Name"`
	Tags []string `json:"tags" title:"Tags"`
}

// Result is the output with repository list
type Result struct {
	Context      Context    `json:"context,omitempty" title:"Context"`
	Registry     string     `json:"registry" title:"Registry"`
	Repositories []RepoInfo `json:"repositories" title:"Repositories"`
	Count        int        `json:"count" title:"Count"`
}

// Error output
type Error struct {
	Context Context `json:"context,omitempty" title:"Context"`
	Error   string  `json:"error" title:"Error"`
}

// Component implements the registry catalog listing
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
		Description: "Registry Catalog",
		Info:        "List all repositories and their tags in a container registry. Supports Docker Hub, GHCR, private registries, and any OCI-compliant registry. Use to discover available images or monitor registry contents.",
		Tags:        []string{"OCI", "Registry", "Container", "Catalog"},
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
	if req.Registry == "" {
		return c.handleError(ctx, handler, req, "registry is required")
	}

	opts := []crane.Option{crane.WithContext(ctx)}
	if req.Insecure {
		opts = append(opts, crane.Insecure)
	}

	repos, err := crane.Catalog(req.Registry, opts...)
	if err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("catalog failed: %v", err))
	}

	repositories := make([]RepoInfo, 0, len(repos))
	for _, repo := range repos {
		fullRepo := fmt.Sprintf("%s/%s", req.Registry, repo)
		tags, err := crane.ListTags(fullRepo, opts...)
		if err != nil {
			tags = []string{}
		}
		repositories = append(repositories, RepoInfo{
			Name: repo,
			Tags: tags,
		})
	}

	return handler(ctx, ResultPort, Result{
		Context:      req.Context,
		Registry:     req.Registry,
		Repositories: repositories,
		Count:        len(repositories),
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
				Registry: "registry.local:5000",
			},
			Position: module.Left,
		},
		{
			Name:   ResultPort,
			Label:  "Result",
			Source: true,
			Configuration: Result{
				Registry: "registry.local:5000",
				Repositories: []RepoInfo{
					{Name: "nginx", Tags: []string{"latest", "1.27"}},
					{Name: "myapp", Tags: []string{"v1.0.0", "v1.1.0"}},
				},
				Count: 2,
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
