package runner

import (
	"crypto/tls"
	"crypto/x509"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

// Session is the state shared by the requests of one sitting: the active environment and runtime variables.
type Session struct {
	Collection *collection.Collection
	// Env is the active environment, or nil for none.
	Env       *collection.Environment
	GlobalEnv *collection.Environment
	// Overrides win over the environment's own values, like bru run --env-var.
	Overrides map[string]string
	// Secrets hold secret environment values by environment and variable name; they never reach a file.
	Secrets map[string]map[string]string
	Runtime map[string]any
	// Process is process.env: the OS environment overlaid by the collection's .env.
	Process           map[string]string
	Client            *http.Client
	Transport         TransportOptions
	rootCAs           *x509.CertPool
	tokens            map[string]*oauth2.Token
	Interactive       bool
	OpenBrowser       func(string) error
	StreamInteractive bool
	StreamMessages    int
	StreamTimeout     time.Duration
	StreamInput       <-chan collection.Message
	OnStream          func(*script.Response)
}

// NewSession starts a session on c with environment env (may be nil).
func NewSession(c *collection.Collection, env *collection.Environment, insecure bool) *Session {
	process := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			process[k] = v
		}
	}
	maps.Copy(process, c.DotEnv)
	jar, _ := cookiejar.New(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Session{
		Collection:     c,
		Env:            env,
		Overrides:      map[string]string{},
		Secrets:        map[string]map[string]string{},
		Runtime:        map[string]any{},
		Process:        process,
		Client:         &http.Client{Transport: transport, Jar: jar},
		tokens:         map[string]*oauth2.Token{},
		StreamMessages: 1,
	}
}

// Fork copies the mutable state so requests can run in the background while the original is shown and edited.
func (s *Session) Fork() *Session {
	f := *s
	if s.Env != nil {
		f.Env = s.Env.Clone()
	}
	if s.GlobalEnv != nil {
		f.GlobalEnv = s.GlobalEnv.Clone()
	}
	f.Overrides = maps.Clone(s.Overrides)
	f.Runtime = deepCopy(s.Runtime).(map[string]any)
	f.tokens = maps.Clone(s.tokens)
	f.Secrets = map[string]map[string]string{}
	for env, vals := range s.Secrets {
		f.Secrets[env] = maps.Clone(vals)
	}
	return &f
}

// deepCopy copies the maps and slices of a script value; scripts mutate objects in place, so a
// fork must not share them with the session it came from.
func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(x))
		for k, e := range x {
			c[k] = deepCopy(e)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, e := range x {
			c[i] = deepCopy(e)
		}
		return c
	}
	return v
}

// Adopt takes over what a fork's scripts changed: runtime variables, secrets, environment values
// and -env-var overrides a script replaced.
func (s *Session) Adopt(f *Session) {
	s.Runtime = f.Runtime
	s.tokens = f.tokens
	s.Secrets = f.Secrets
	s.Overrides = f.Overrides
	if s.Env != nil && f.Env != nil && s.Env.Path == f.Env.Path {
		s.Env.Vars = f.Env.Vars
	}
}

// Scope builds the variables one request sees.
func (s *Session) Scope(r *collection.Request) *vars.Scope {
	scope := &vars.Scope{
		Global:      map[string]string{},
		Collection:  enabled(s.Collection.Root.Defaults.Vars),
		Environment: s.envVars(),
		Folder:      map[string]string{},
		Request:     map[string]string{},
		Runtime:     s.Runtime,
		Process:     s.Process,
	}
	if s.GlobalEnv != nil {
		for _, value := range s.GlobalEnv.Vars {
			if value.Enabled && value.Secret {
				if secret, ok := s.SecretValues(s.GlobalEnv)[value.Name]; ok {
					scope.Global[value.Name] = secret
				}
			} else if value.Enabled {
				scope.Global[value.Name] = value.Value
			}
		}
	}
	if r != nil {
		for _, f := range r.Chain()[1:] {
			maps.Copy(scope.Folder, enabled(f.Defaults.Vars))
		}
		scope.Request = enabled(r.Vars)
	}
	return scope
}

func (s *Session) envVars() map[string]string {
	out := map[string]string{}
	if s.Env != nil {
		secrets := s.Secrets[s.Env.Name]
		for _, v := range s.Env.Vars {
			if !v.Enabled {
				continue
			}
			if v.Secret {
				if val, ok := secrets[v.Name]; ok {
					out[v.Name] = val
				}
				continue
			}
			out[v.Name] = v.Value
		}
	}
	maps.Copy(out, s.Overrides)
	return out
}

func enabled(kvs []collection.KV) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		if kv.Enabled {
			out[kv.Name] = kv.Value
		}
	}
	return out
}

func (s *Session) envName() string {
	if s.Env == nil {
		return ""
	}
	return s.Env.Name
}

// SetSecret stores a secret value of the active environment for this session.
func (s *Session) SetSecret(name, value string) {
	if s.Env == nil {
		return
	}
	s.SetEnvironmentSecret(s.Env, name, value)
}

func (s *Session) environmentSecretKey(env *collection.Environment) string {
	if env == s.Env {
		return env.Name
	}
	if s.GlobalEnv != nil && (env == s.GlobalEnv || env.Path != "" && env.Path == s.GlobalEnv.Path) {
		return "global:" + env.Name
	}
	return env.Name
}

func (s *Session) SecretValues(env *collection.Environment) map[string]string {
	if env == nil {
		return nil
	}
	return s.Secrets[s.environmentSecretKey(env)]
}

func (s *Session) SetEnvironmentSecret(env *collection.Environment, name, value string) {
	if env == nil {
		return
	}
	key := s.environmentSecretKey(env)
	if s.Secrets == nil {
		s.Secrets = map[string]map[string]string{}
	}
	if s.Secrets[key] == nil {
		s.Secrets[key] = map[string]string{}
	}
	s.Secrets[key][name] = value
}
