package containerregistry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/rs/zerolog/log"
	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/module"
	moduleregistry "github.com/tiny-systems/module/registry"
)

const (
	ComponentName  = "container_registry"
	StartPort      = "start"
	ReplicatePort  = "replicate"
	EventPort      = "event"
	ErrorPort      = "error"
)

// Context type alias for schema generation
type Context any

// Settings configures the component
type Settings struct {
	EnableErrorPort bool `json:"enableErrorPort" title:"Enable Error Port" description:"Output errors to error port instead of failing"`
}

// Start configures and starts the registry server
type Start struct {
	Context Context `json:"context,omitempty" configurable:"true" title:"Context"`
	Port    int     `json:"port" required:"true" title:"Port" description:"Port to listen on"`
}

// ReplicateRequest triggers pulling an image from a remote registry into this one
type ReplicateRequest struct {
	Context  Context `json:"context,omitempty" configurable:"true" title:"Context" description:"Arbitrary context to pass through"`
	Source   string  `json:"source" required:"true" title:"Source Image" description:"Remote image reference (e.g., docker.io/library/nginx:latest)"`
	Target   string  `json:"target" required:"true" title:"Target Repository" description:"Local repository name (e.g., nginx:latest). Will be stored in this registry."`
	Insecure bool    `json:"insecure,omitempty" title:"Insecure Source" description:"Allow insecure (HTTP) for the source registry"`
}

// RegistryEvent is emitted on push, pull, or replicate operations
type RegistryEvent struct {
	Context    Context `json:"context,omitempty" configurable:"true" title:"Context"`
	Action     string  `json:"action" title:"Action" description:"push, pull, or replicate"`
	Repository string  `json:"repository" title:"Repository"`
	Reference  string  `json:"reference" title:"Reference" description:"Tag or digest"`
}

// Error output
type Error struct {
	Context Context `json:"context,omitempty" title:"Context"`
	Error   string  `json:"error" title:"Error"`
}

// Component implements the container registry server
type Component struct {
	settings     Settings
	settingsLock sync.RWMutex

	cancelFunc     context.CancelFunc
	cancelFuncLock sync.Mutex

	storagePath string
	nodeName    string
	listenPort  int
}

func (c *Component) Instance() module.Component {
	return &Component{}
}

func (c *Component) GetInfo() module.ComponentInfo {
	return module.ComponentInfo{
		Name:        ComponentName,
		Description: "Container Registry",
		Info:        "OCI-compliant container registry server with image replication. Starts an HTTP registry server that containers can push/pull images to. Use the replicate port to pull images from remote registries into local storage. Designed for edge caching, air-gapped environments, or staging registries within your cluster. Mount a PVC with storage.enabled=true for persistence.",
		Tags:        []string{"OCI", "Registry", "Container", "Server", "Cache", "Edge"},
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

	case v1alpha1.IdentityPort:
		id, ok := msg.(v1alpha1.NodeIdentity)
		if !ok {
			return fmt.Errorf("invalid identity")
		}
		base := os.Getenv("STORAGE_PATH")
		if base == "" {
			base = "/tmp"
		}
		c.storagePath = filepath.Join(base, id.NodeName)
		c.nodeName = id.NodeName
		if err := os.MkdirAll(c.storagePath, 0o755); err != nil {
			return fmt.Errorf("failed to create storage directory: %w", err)
		}
		return nil

	case StartPort:
		in, ok := msg.(Start)
		if !ok {
			return fmt.Errorf("invalid start message")
		}
		return c.start(ctx, in)

	case ReplicatePort:
		in, ok := msg.(ReplicateRequest)
		if !ok {
			return fmt.Errorf("invalid replicate request")
		}
		return c.replicate(ctx, handler, in)
	}

	return fmt.Errorf("unknown port: %s", port)
}

func (c *Component) start(ctx context.Context, cfg Start) any {
	c.cancelFuncLock.Lock()
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	serverCtx, cancel := context.WithCancel(ctx)
	c.cancelFunc = cancel
	c.cancelFuncLock.Unlock()

	c.listenPort = cfg.Port

	regHandler := registry.New()

	addr := fmt.Sprintf(":%d", cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	server := &http.Server{Handler: regHandler}

	go func() {
		<-serverCtx.Done()
		server.Close()
	}()

	log.Info().Int("port", cfg.Port).Msg("container registry started")

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

func (c *Component) replicate(ctx context.Context, handler module.Handler, req ReplicateRequest) any {
	if req.Source == "" || req.Target == "" {
		return c.handleError(ctx, handler, req.Context, "source and target are required")
	}

	// Build the local target reference pointing at this registry
	localTarget := fmt.Sprintf("localhost:%d/%s", c.listenPort, req.Target)

	srcOpts := []crane.Option{crane.WithContext(ctx)}
	if req.Insecure {
		srcOpts = append(srcOpts, crane.Insecure)
	}

	// Pull from remote
	img, err := crane.Pull(req.Source, srcOpts...)
	if err != nil {
		return c.handleError(ctx, handler, req.Context, fmt.Sprintf("failed to pull %s: %v", req.Source, err))
	}

	// Push to local registry (always insecure since it's localhost)
	if err := crane.Push(img, localTarget, crane.Insecure); err != nil {
		return c.handleError(ctx, handler, req.Context, fmt.Sprintf("failed to push to local registry: %v", err))
	}

	log.Info().Str("source", req.Source).Str("target", localTarget).Msg("image replicated")

	return handler(ctx, EventPort, RegistryEvent{
		Context:    req.Context,
		Action:     "replicate",
		Repository: req.Target,
		Reference:  req.Source,
	})
}

func (c *Component) handleError(ctx context.Context, handler module.Handler, reqContext Context, errMsg string) any {
	c.settingsLock.RLock()
	enableErrorPort := c.settings.EnableErrorPort
	c.settingsLock.RUnlock()

	if enableErrorPort {
		return handler(ctx, ErrorPort, Error{
			Context: reqContext,
			Error:   errMsg,
		})
	}
	return fmt.Errorf("%s", errMsg)
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
			Name:          v1alpha1.IdentityPort,
			Label:         "Identity",
			Configuration: v1alpha1.NodeIdentity{},
		},
		{
			Name:  StartPort,
			Label: "Start",
			Configuration: Start{
				Port: 5000,
			},
			Position: module.Left,
		},
		{
			Name:  ReplicatePort,
			Label: "Replicate",
			Configuration: ReplicateRequest{
				Source: "docker.io/library/nginx:latest",
				Target: "nginx:latest",
			},
			Position: module.Left,
		},
		{
			Name:   EventPort,
			Label:  "Event",
			Source: true,
			Configuration: RegistryEvent{
				Action:     "replicate",
				Repository: "nginx",
				Reference:  "docker.io/library/nginx:latest",
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
	moduleregistry.Register(&Component{})
}
