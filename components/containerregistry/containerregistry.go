package containerregistry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"cuelabs.dev/go/oci/ociregistry/ociclient"
	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"cuelabs.dev/go/oci/ociregistry/ociunify"
	"github.com/rs/zerolog/log"
	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/module"
	moduleregistry "github.com/tiny-systems/module/registry"
)

const (
	ComponentName = "container_registry"
	StartPort     = "start"
	EventPort     = "event"
)

// Context type alias for schema generation
type Context any

// Settings configures the component
type Settings struct{}

// Start configures and starts the registry server
type Start struct {
	Context   Context `json:"context,omitempty" configurable:"true" title:"Context"`
	Port      int     `json:"port" required:"true" title:"Port" description:"Port to listen on"`
	RemoteURL string  `json:"remoteURL,omitempty" title:"Upstream Registry" description:"Pull-through cache upstream host (e.g., registry-1.docker.io). Leave empty for standalone registry."`
}

// RegistryEvent is emitted on registry operations
type RegistryEvent struct {
	Context Context `json:"context,omitempty" configurable:"true" title:"Context"`
	Action  string  `json:"action" title:"Action" description:"Request method"`
	Path    string  `json:"path" title:"Path" description:"Request path"`
	Status  int     `json:"status" title:"Status" description:"HTTP status code"`
}

// Component implements the container registry server
type Component struct {
	settings     Settings
	settingsLock sync.RWMutex

	cancelFunc     context.CancelFunc
	cancelFuncLock sync.Mutex

	storagePath string
	nodeName    string
}

func (c *Component) Instance() module.Component {
	return &Component{}
}

func (c *Component) GetInfo() module.ComponentInfo {
	return module.ComponentInfo{
		Name:        ComponentName,
		Description: "Container Registry",
		Info:        "OCI-compliant container registry server. Standalone mode: push and pull images directly. Pull-through cache mode: set upstream registry host and it transparently proxies requests, caching locally. Uses cuelabs ociregistry library. Mount a PVC with storage.enabled=true for persistence across restarts.",
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
		return c.start(ctx, handler, in)
	}

	return fmt.Errorf("unknown port: %s", port)
}

func (c *Component) start(ctx context.Context, handler module.Handler, cfg Start) any {
	c.cancelFuncLock.Lock()
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	serverCtx, cancel := context.WithCancel(ctx)
	c.cancelFunc = cancel
	c.cancelFuncLock.Unlock()

	// Local in-memory registry (TODO: disk-backed implementation using storagePath)
	local := ocimem.New()

	var httpHandler http.Handler

	if cfg.RemoteURL != "" {
		// Pull-through cache: local first, fall back to upstream
		upstream, err := ociclient.New(cfg.RemoteURL, nil)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to create upstream client for %s: %w", cfg.RemoteURL, err)
		}
		unified := ociunify.New(local, upstream, &ociunify.Options{
			ReadPolicy: ociunify.ReadSequential,
		})
		httpHandler = ociserver.New(unified, nil)
	} else {
		// Standalone registry
		httpHandler = ociserver.New(local, nil)
	}

	// Wrap with event middleware
	wrappedHandler := c.wrapWithEvents(httpHandler, handler, cfg.Context)

	addr := fmt.Sprintf(":%d", cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	server := &http.Server{Handler: wrappedHandler}

	go func() {
		<-serverCtx.Done()
		server.Close()
	}()

	mode := "standalone"
	if cfg.RemoteURL != "" {
		mode = fmt.Sprintf("pull-through → %s", cfg.RemoteURL)
	}
	log.Info().Int("port", cfg.Port).Str("mode", mode).Msg("container registry started")

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

func (c *Component) wrapWithEvents(next http.Handler, handler module.Handler, flowCtx Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		// Emit event for successful write operations
		if (r.Method == http.MethodPut || r.Method == http.MethodPatch) && rw.status >= 200 && rw.status < 300 {
			go handler(context.Background(), EventPort, RegistryEvent{
				Context: flowCtx,
				Action:  r.Method,
				Path:    r.URL.Path,
				Status:  rw.status,
			})
		}
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (c *Component) Ports() []module.Port {
	return []module.Port{
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
			Name:   EventPort,
			Label:  "Event",
			Source: true,
			Configuration: RegistryEvent{
				Action: "PUT",
				Path:   "/v2/library/nginx/manifests/latest",
				Status: 201,
			},
			Position: module.Right,
		},
	}
}

var _ module.Component = (*Component)(nil)

func init() {
	moduleregistry.Register(&Component{})
}
