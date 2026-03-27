package containerregistry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/google/go-containerregistry/pkg/registry"
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
	Context Context `json:"context,omitempty" configurable:"true" title:"Context"`
	Port    int     `json:"port" required:"true" title:"Port" description:"Port to listen on" colSpan:"col-span-6"`
}

// RegistryEvent is emitted when an image is pushed or pulled
type RegistryEvent struct {
	Context    Context `json:"context,omitempty" configurable:"true" title:"Context"`
	Action     string  `json:"action" title:"Action" description:"push or pull"`
	Repository string  `json:"repository" title:"Repository"`
	Reference  string  `json:"reference" title:"Reference" description:"Tag or digest"`
	Method     string  `json:"method" title:"Method" description:"HTTP method"`
	Path       string  `json:"path" title:"Path" description:"Request path"`
	Status     int     `json:"status" title:"Status" description:"HTTP status code"`
}

// Component implements the container registry server
type Component struct {
	settings     Settings
	settingsLock sync.RWMutex

	cancelFunc     context.CancelFunc
	cancelFuncLock sync.Mutex

	handler module.Handler
	startCfg Start
}

func (c *Component) Instance() module.Component {
	return &Component{}
}

func (c *Component) GetInfo() module.ComponentInfo {
	return module.ComponentInfo{
		Name:        ComponentName,
		Description: "Container Registry",
		Info:        "OCI-compliant container registry server. Starts an HTTP server that implements the OCI Distribution Spec. Containers can push and pull images to/from this registry. Use for local edge caching, air-gapped environments, or as a staging registry within your cluster.",
		Tags:        []string{"OCI", "Registry", "Container", "Server"},
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

	case StartPort:
		in, ok := msg.(Start)
		if !ok {
			return fmt.Errorf("invalid start message")
		}
		c.handler = handler
		c.startCfg = in
		return c.start(ctx, handler, in)
	}

	return fmt.Errorf("unknown port: %s", port)
}

func (c *Component) start(ctx context.Context, handler module.Handler, cfg Start) any {
	// Stop previous instance if running
	c.cancelFuncLock.Lock()
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
	serverCtx, cancel := context.WithCancel(ctx)
	c.cancelFunc = cancel
	c.cancelFuncLock.Unlock()

	regHandler := registry.New()

	// Wrap with event-emitting middleware
	wrappedHandler := c.wrapWithEvents(regHandler, handler, cfg.Context)

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

	log.Info().Int("port", cfg.Port).Msg("container registry started")

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

// wrapWithEvents wraps the registry handler to emit events on push operations
func (c *Component) wrapWithEvents(next http.Handler, handler module.Handler, flowCtx Context) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the response status
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		// Emit event for manifest puts (image push completion)
		if r.Method == http.MethodPut && rw.status >= 200 && rw.status < 300 {
			event := RegistryEvent{
				Context:   flowCtx,
				Action:    "push",
				Method:    r.Method,
				Path:      r.URL.Path,
				Status:    rw.status,
			}
			// Fire and forget — don't block the HTTP response
			go handler(context.Background(), EventPort, event)
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
				Action:     "push",
				Repository: "myapp",
				Reference:  "v1.0.0",
				Method:     "PUT",
				Path:       "/v2/myapp/manifests/v1.0.0",
				Status:     201,
			},
			Position: module.Right,
		},
	}
}

var _ module.Component = (*Component)(nil)

func init() {
	moduleregistry.Register(&Component{})
}
