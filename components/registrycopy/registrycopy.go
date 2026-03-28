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
	Context          Context `json:"context,omitempty" configurable:"true" title:"Context" description:"Arbitrary context to pass through"`
	Source           string  `json:"source" required:"true" title:"Source" description:"Source image reference (e.g. docker.io/library/nginx:latest)"`
	Target           string  `json:"target" required:"true" title:"Target" description:"Target image reference (e.g. localhost:5000/nginx:latest)"`
	Insecure         bool    `json:"insecure,omitempty" title:"Insecure" description:"Allow insecure (HTTP) connections for target registry"`
	DockerConfigJSON string  `json:"dockerConfigJSON,omitempty" configurable:"true" title:"Docker Config JSON" description:"Raw .dockerconfigjson content for registry auth. Read from a K8s secret via kubernetes-module and pass through edges."`
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
		Info:        "Copy a container image from one registry to another. Supports auth via dockerConfigJSON (pass regcred secret content through edges). Set insecure=true when target has no TLS.",
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

	keychain := buildKeychain(req.DockerConfigJSON)

	srcOpts := []crane.Option{crane.WithContext(ctx), crane.WithAuthFromKeychain(keychain)}
	dstOpts := []crane.Option{crane.WithContext(ctx), crane.WithAuthFromKeychain(keychain)}
	if req.Insecure {
		dstOpts = append(dstOpts, crane.Insecure)
	}

	img, err := crane.Pull(req.Source, srcOpts...)
	if err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("pull failed: %v", err))
	}

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

func buildKeychain(dockerConfigJSON string) authn.Keychain {
	if dockerConfigJSON == "" {
		return authn.DefaultKeychain
	}

	kc := &configKeychain{creds: make(map[string]authn.AuthConfig)}

	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal([]byte(dockerConfigJSON), &cfg); err != nil {
		return authn.DefaultKeychain
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
		reg = strings.TrimPrefix(reg, "https://")
		reg = strings.TrimPrefix(reg, "http://")
		reg = strings.TrimSuffix(reg, "/")
		kc.creds[reg] = ac
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

type configKeychain struct {
	creds map[string]authn.AuthConfig
}

func (k *configKeychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	if ac, ok := k.creds[res.RegistryStr()]; ok {
		return authn.FromConfig(ac), nil
	}
	return authn.Anonymous, nil
}

var _ module.Component = (*Component)(nil)

func init() {
	registry.Register(&Component{})
}
