package registrycopy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
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

// RegistryAuth holds credentials for a single registry
type RegistryAuth struct {
	Username string `json:"username,omitempty" title:"Username"`
	Password string `json:"password,omitempty" title:"Password" description:"Password or token"`
}

type Request struct {
	Context        Context       `json:"context,omitempty" configurable:"true" title:"Context" description:"Arbitrary context to pass through"`
	Source         string        `json:"source" required:"true" title:"Source" description:"Source image reference (e.g. docker.io/library/nginx:latest)"`
	Target         string        `json:"target" required:"true" title:"Target" description:"Target image reference (e.g. localhost:5000/nginx:latest)"`
	Insecure       bool          `json:"insecure,omitempty" title:"Insecure" description:"Allow insecure (HTTP) connections for target registry"`
	SourceAuth     *RegistryAuth `json:"sourceAuth,omitempty" title:"Source Auth" description:"Credentials for source registry"`
	TargetAuth     *RegistryAuth `json:"targetAuth,omitempty" title:"Target Auth" description:"Credentials for target registry"`
	DockerConfigJSON string      `json:"dockerConfigJSON,omitempty" title:"Docker Config JSON" description:"Raw .dockerconfigjson content (from regcred secret). Auto-matches credentials by registry hostname."`
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
		Info:        "Copy a container image from one registry to another. Supports auth via explicit credentials or dockerconfigjson (regcred secret). Set insecure=true when target has no TLS.",
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

	keychain := c.buildKeychain(req)

	srcOpts := []crane.Option{crane.WithContext(ctx), crane.WithAuthFromKeychain(keychain)}
	dstOpts := []crane.Option{crane.WithContext(ctx), crane.WithAuthFromKeychain(keychain)}
	if req.Insecure {
		dstOpts = append(dstOpts, crane.Insecure)
	}

	// Pull from source
	img, err := crane.Pull(req.Source, srcOpts...)
	if err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("pull failed: %v", err))
	}

	// Push to target
	if err := crane.Push(img, req.Target, dstOpts...); err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("push failed: %v", err))
	}

	digest, err := crane.Digest(req.Target, dstOpts...)
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

// buildKeychain constructs an authn.Keychain from the request's auth fields.
func (c *Component) buildKeychain(req Request) authn.Keychain {
	kc := &multiKeychain{}

	// Explicit source/target auth takes priority
	if req.SourceAuth != nil && req.SourceAuth.Username != "" {
		if reg := registryFromRef(req.Source); reg != "" {
			kc.add(reg, req.SourceAuth.Username, req.SourceAuth.Password)
		}
	}
	if req.TargetAuth != nil && req.TargetAuth.Username != "" {
		if reg := registryFromRef(req.Target); reg != "" {
			kc.add(reg, req.TargetAuth.Username, req.TargetAuth.Password)
		}
	}

	// Parse dockerconfigjson if provided
	if req.DockerConfigJSON != "" {
		kc.addDockerConfig(req.DockerConfigJSON)
	}

	return kc
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

// --- keychain implementation ---

type multiKeychain struct {
	creds map[string]authn.AuthConfig
}

func (k *multiKeychain) add(registry, username, password string) {
	if k.creds == nil {
		k.creds = make(map[string]authn.AuthConfig)
	}
	k.creds[registry] = authn.AuthConfig{Username: username, Password: password}
}

func (k *multiKeychain) addDockerConfig(raw string) {
	if k.creds == nil {
		k.creds = make(map[string]authn.AuthConfig)
	}

	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return
	}
	for reg, auth := range cfg.Auths {
		ac := authn.AuthConfig{Username: auth.Username, Password: auth.Password}
		if ac.Username == "" && auth.Auth != "" {
			if decoded, err := base64.StdEncoding.DecodeString(auth.Auth); err == nil {
				parts := strings.SplitN(string(decoded), ":", 2)
				if len(parts) == 2 {
					ac.Username = parts[0]
					ac.Password = parts[1]
				}
			}
		}
		// Normalize: strip https:// prefix if present
		reg = strings.TrimPrefix(reg, "https://")
		reg = strings.TrimPrefix(reg, "http://")
		reg = strings.TrimSuffix(reg, "/")
		if _, exists := k.creds[reg]; !exists {
			k.creds[reg] = ac
		}
	}
}

func (k *multiKeychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	if k.creds == nil {
		return authn.Anonymous, nil
	}
	registry := res.RegistryStr()
	if ac, ok := k.creds[registry]; ok {
		return authn.FromConfig(ac), nil
	}
	return authn.Anonymous, nil
}

func registryFromRef(ref string) string {
	r, err := name.ParseReference(ref)
	if err != nil {
		return ""
	}
	return r.Context().RegistryStr()
}

var _ module.Component = (*Component)(nil)

func init() {
	registry.Register(&Component{})
}
