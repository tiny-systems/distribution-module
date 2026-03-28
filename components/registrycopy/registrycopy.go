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
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	Context         Context `json:"context,omitempty" configurable:"true" title:"Context" description:"Arbitrary context to pass through"`
	Source          string  `json:"source" required:"true" title:"Source" description:"Source image reference (e.g. docker.io/library/nginx:latest)"`
	Target          string  `json:"target" required:"true" title:"Target" description:"Target image reference (e.g. localhost:5000/nginx:latest)"`
	Insecure        bool    `json:"insecure,omitempty" title:"Insecure" description:"Allow insecure (HTTP) connections for target registry"`
	SecretName      string  `json:"secretName,omitempty" title:"Secret Name" description:"K8s docker-registry secret name (e.g. regcred). Read automatically from the cluster."`
	SecretNamespace string  `json:"secretNamespace,omitempty" title:"Secret Namespace" description:"Namespace of the secret. Defaults to source deployment namespace from context."`
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

	k8sClient     client.Client
	k8sClientLock sync.RWMutex
}

func (c *Component) Instance() module.Component {
	return &Component{}
}

func (c *Component) GetInfo() module.ComponentInfo {
	return module.ComponentInfo{
		Name:        ComponentName,
		Description: "Registry Copy",
		Info:        "Copy a container image from one registry to another. Supports auth via K8s docker-registry secrets (regcred). Set secretName to auto-read credentials from the cluster. Set insecure=true when target has no TLS.",
		Tags:        []string{"OCI", "Registry", "Container", "Copy", "Replicate"},
	}
}

func (c *Component) Handle(ctx context.Context, handler module.Handler, port string, msg any) any {
	switch port {
	case v1alpha1.ClientPort:
		if k8sProvider, ok := msg.(module.K8sClient); ok {
			c.k8sClientLock.Lock()
			c.k8sClient = k8sProvider.GetK8sClient()
			c.k8sClientLock.Unlock()
		}
		return nil

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

	keychain, err := c.buildKeychain(ctx, req)
	if err != nil {
		return c.handleError(ctx, handler, req, fmt.Sprintf("auth setup failed: %v", err))
	}

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

func (c *Component) buildKeychain(ctx context.Context, req Request) (authn.Keychain, error) {
	if req.SecretName == "" {
		return authn.DefaultKeychain, nil
	}

	c.k8sClientLock.RLock()
	k8sClient := c.k8sClient
	c.k8sClientLock.RUnlock()

	if k8sClient == nil {
		return nil, fmt.Errorf("K8s client not available, cannot read secret %q", req.SecretName)
	}

	ns := req.SecretNamespace
	if ns == "" {
		ns = "default"
	}

	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: req.SecretName}, secret); err != nil {
		return nil, fmt.Errorf("failed to read secret %s/%s: %v", ns, req.SecretName, err)
	}

	dockerCfg, ok := secret.Data[".dockerconfigjson"]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s has no .dockerconfigjson key", ns, req.SecretName)
	}

	kc := &configKeychain{}
	kc.parse(string(dockerCfg))
	return kc, nil
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
			Name:     v1alpha1.ClientPort,
			Label:    "Client",
			Position: module.Left,
		},
		{
			Name:          v1alpha1.SettingsPort,
			Label:         "Settings",
			Configuration: Settings{},
		},
		{
			Name:  RequestPort,
			Label: "Request",
			Configuration: Request{
				Source:     "docker.io/library/nginx:latest",
				Target:     "localhost:5000/nginx:latest",
				SecretName: "regcred",
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

type configKeychain struct {
	creds map[string]authn.AuthConfig
}

func (k *configKeychain) parse(raw string) {
	k.creds = make(map[string]authn.AuthConfig)

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
		reg = strings.TrimPrefix(reg, "https://")
		reg = strings.TrimPrefix(reg, "http://")
		reg = strings.TrimSuffix(reg, "/")
		k.creds[reg] = ac
	}
}

func (k *configKeychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	if k.creds == nil {
		return authn.Anonymous, nil
	}
	if ac, ok := k.creds[res.RegistryStr()]; ok {
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
