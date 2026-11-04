package runner

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
)

// Curl renders the sent request as a curl command; Mask it first to hide credentials.
func Curl(sent *script.Request) string {
	var b strings.Builder
	method := sent.Method
	if !token.MatchString(method) {
		method = shellQuote(method)
	}
	fmt.Fprintf(&b, "curl -X %s %s", method, shellQuote(sent.URL))
	for _, h := range sent.Headers {
		fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(h.Name+": "+h.Value))
	}
	if sent.Body != "" {
		fmt.Fprintf(&b, " \\\n  --data-raw %s", shellQuote(sent.Body))
	}
	return b.String()
}

var token = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// LooksSecret reports a variable name that suggests a credential.
func LooksSecret(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"password", "passwd", "secret", "token", "apikey", "api_key", "credential", "private"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Mask returns a copy of sent with credentials replaced by ****: Authorization, Proxy-Authorization
// and Cookie headers (keeping the auth scheme), and every value of the request's auth, of secret
// environment variables and of variables whose names look secret, wherever it appears.
func (s *Session) Mask(r *collection.Request, sent *script.Request) *script.Request {
	scope := s.Scope(r)
	queryCredentials := map[string]bool{}
	headerCredentials := map[string]bool{}
	if r != nil {
		auth := s.resolvedAuth(r, scope)
		if auth.Mode == "apikey" {
			if auth.Placement == "query" {
				queryCredentials[scope.Interpolate(auth.Key)] = true
			} else {
				headerCredentials[strings.ToLower(scope.Interpolate(auth.Key))] = true
			}
		} else if auth.Mode == "oauth2" {
			if key, queryPlacement := oauth2QueryKey(auth); queryPlacement {
				queryCredentials[key] = true
			}
		}
	}
	var values []string
	note := func(v string) {
		if v = scope.Interpolate(v); len(v) >= 4 && !slices.Contains(values, v) {
			values = append(values, v, encodeComponent(v), url.QueryEscape(v))
		}
	}
	if r != nil {
		if a, _ := EffectiveAuth(r); a.Mode != "none" {
			note(a.Password)
			note(a.Token)
			note(a.Value)
			for key, value := range a.Options {
				if LooksSecret(key) {
					note(value)
				}
			}
		}
	}
	if s.Env != nil {
		for _, v := range s.Env.Vars {
			if v.Secret {
				note(s.Secrets[s.Env.Name][v.Name])
			}
		}
	}
	if s.GlobalEnv != nil {
		for _, variable := range s.GlobalEnv.Vars {
			if variable.Secret {
				note(s.SecretValues(s.GlobalEnv)[variable.Name])
			}
		}
	}
	for _, v := range scope.All() {
		if LooksSecret(v.Name) {
			note(v.Raw)
		}
	}
	maskedURL := sent.URL
	if u, err := url.Parse(sent.URL); err == nil {
		parts := strings.Split(u.RawQuery, "&")
		for i, part := range parts {
			key, value, found := strings.Cut(part, "=")
			name, _ := url.QueryUnescape(key)
			if found && queryCredentials[name] {
				decoded, _ := url.QueryUnescape(value)
				note(decoded)
				parts[i] = key + "=****"
			}
		}
		u.RawQuery = strings.Join(parts, "&")
		maskedURL = u.String()
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	hide := func(str string) string {
		for _, v := range values {
			if len(v) >= 4 {
				str = strings.ReplaceAll(str, v, "****")
			}
		}
		return str
	}
	c := *sent
	c.URL, c.Body = hide(maskedURL), hide(sent.Body)
	c.Headers = slices.Clone(sent.Headers)
	for i, h := range c.Headers {
		switch strings.ToLower(h.Name) {
		case "authorization", "proxy-authorization":
			scheme, _, found := strings.Cut(h.Value, " ")
			if !found || !token.MatchString(scheme) {
				scheme = ""
			}
			c.Headers[i].Value = strings.TrimSpace(scheme + " ****")
		case "cookie", "x-wsse":
			c.Headers[i].Value = "****"
		default:
			if headerCredentials[strings.ToLower(h.Name)] {
				c.Headers[i].Value = "****"
			} else {
				c.Headers[i].Value = hide(h.Value)
			}
		}
	}
	return &c
}
