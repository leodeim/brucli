package runner

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/go-ntlmssp"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/icholy/digest"

	"github.com/leodeim/brucli/internal/collection"
	"github.com/leodeim/brucli/internal/script"
	"github.com/leodeim/brucli/internal/vars"
)

func authValue(auth collection.Auth, names ...string) string {
	for _, name := range names {
		if value, ok := auth.Options[name]; ok {
			return value
		}
	}
	if value := auth.Option(names...); value != "" {
		return value
	}
	for _, name := range names {
		switch name {
		case "username":
			return auth.Username
		case "password":
			return auth.Password
		case "token":
			return auth.Token
		}
	}
	return ""
}

func (s *Session) resolvedAuth(r *collection.Request, scope *vars.Scope) collection.Auth {
	auth, _ := EffectiveAuth(r)
	auth.Options = maps.Clone(auth.Options)
	for key, value := range auth.Options {
		auth.Options[key] = scope.Interpolate(value)
	}
	auth.Username, auth.Password, auth.Token = scope.Interpolate(auth.Username), scope.Interpolate(auth.Password), scope.Interpolate(auth.Token)
	return auth
}

func (s *Session) prepareAuth(ctx context.Context, r *collection.Request, sent *script.Request, scope *vars.Scope) error {
	auth := s.resolvedAuth(r, scope)
	if !auth.Supported() {
		return fmt.Errorf("unsupported authentication %q; request was not sent", auth.Mode)
	}
	if auth.Mode == "ntlm" {
		user := authValue(auth, "username")
		if domain := authValue(auth, "domain"); domain != "" && !strings.Contains(user, "\\") {
			user = domain + "\\" + user
		}
		sent.SetHeader("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+authValue(auth, "password"))))
	}
	if auth.Mode == "oauth2" {
		return s.oauth2Auth(ctx, auth, sent, scope)
	}
	return nil
}

type signingTransport struct {
	base   http.RoundTripper
	origin *url.URL
	sign   func(*http.Request) error
	record func(*http.Request)
}

type originAuthTransport struct {
	base, authorized http.RoundTripper
	origin           *url.URL
}

func (t originAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if sameOrigin(req.URL, t.origin) {
		return t.authorized.RoundTrip(req)
	}
	copy := req.Clone(req.Context())
	copy.Header.Del("Authorization")
	copy.Header.Del("X-WSSE")
	return t.base.RoundTrip(copy)
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (t signingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	if !sameOrigin(req.URL, t.origin) {
		copy.Header.Del("Authorization")
		copy.Header.Del("X-WSSE")
	} else if err := t.sign(copy); err != nil {
		return nil, err
	}
	if t.record != nil && sameOrigin(req.URL, t.origin) {
		t.record(copy)
	}
	return t.base.RoundTrip(copy)
}

func (s *Session) authTransport(ctx context.Context, r *collection.Request, req *http.Request, base http.RoundTripper, scope *vars.Scope) (http.RoundTripper, error) {
	auth := s.resolvedAuth(r, scope)
	switch auth.Mode {
	case "digest":
		return &digest.Transport{Username: authValue(auth, "username"), Password: authValue(auth, "password"), Transport: base, Jar: s.Client.Jar, NoReuse: true}, nil
	case "ntlm":
		return ntlmssp.Negotiator{RoundTripper: base}, nil
	case "oauth1":
		return signingTransport{base: base, origin: req.URL, sign: func(req *http.Request) error { return s.signOAuth1(req, auth) }}, nil
	case "wsse":
		return signingTransport{base: base, origin: req.URL, sign: func(req *http.Request) error {
			nonce := make([]byte, 20)
			if _, err := rand.Read(nonce); err != nil {
				return err
			}
			created := time.Now().UTC().Format(time.RFC3339)
			hash := sha1.New()
			hash.Write(nonce)
			io.WriteString(hash, created)
			io.WriteString(hash, authValue(auth, "password"))
			req.Header.Set("Authorization", `WSSE profile="UsernameToken"`)
			req.Header.Set("X-WSSE", fmt.Sprintf(`UsernameToken Username=%q, PasswordDigest=%q, Nonce=%q, Created=%q`, authValue(auth, "username"), base64.StdEncoding.EncodeToString(hash.Sum(nil)), base64.StdEncoding.EncodeToString(nonce), created))
			return nil
		}}, nil
	case "awsv4":
		service, region := authValue(auth, "service"), authValue(auth, "region")
		if strings.HasSuffix(req.URL.Hostname(), ".amazonaws.com") {
			parts := strings.Split(strings.TrimSuffix(req.URL.Hostname(), ".amazonaws.com"), ".")
			if len(parts) >= 2 {
				service = cmp.Or(service, parts[len(parts)-2])
				region = cmp.Or(region, parts[len(parts)-1])
			} else {
				service = cmp.Or(service, parts[0])
				region = cmp.Or(region, "us-east-1")
			}
		}
		if service == "" {
			return nil, errors.New("AWS signing requires a service")
		}
		accessKey, secretKey, token := authValue(auth, "accessKeyId"), authValue(auth, "secretAccessKey"), authValue(auth, "sessionToken")
		if accessKey == "" {
			accessKey = s.Process["AWS_ACCESS_KEY_ID"]
			secretKey = s.Process["AWS_SECRET_ACCESS_KEY"]
			token = s.Process["AWS_SESSION_TOKEN"]
		}
		credentials := aws.Credentials{AccessKeyID: accessKey, SecretAccessKey: secretKey, SessionToken: token}
		if accessKey == "" || secretKey == "" {
			options := []func(*awsconfig.LoadOptions) error{awsconfig.WithHTTPClient(&http.Client{Transport: base})}
			if profile := authValue(auth, "profileName"); profile != "" {
				options = append(options, awsconfig.WithSharedConfigProfile(profile))
			}
			config, err := awsconfig.LoadDefaultConfig(ctx, options...)
			if err != nil {
				return nil, err
			}
			region = cmp.Or(region, config.Region)
			credentials, err = config.Credentials.Retrieve(ctx)
			if err != nil {
				return nil, err
			}
		}
		region = cmp.Or(region, s.Process["AWS_REGION"], s.Process["AWS_DEFAULT_REGION"])
		if region == "" {
			return nil, errors.New("AWS signing requires a region")
		}
		return signingTransport{base: base, origin: req.URL, sign: func(req *http.Request) error {
			body, err := requestBody(req)
			if err != nil {
				return err
			}
			hash := sha256.Sum256(body)
			return v4.NewSigner().SignHTTP(req.Context(), credentials, req, hex.EncodeToString(hash[:]), service, region, time.Now())
		}}, nil
	case "akamai-edgegrid":
		return signingTransport{base: base, origin: req.URL, sign: func(req *http.Request) error { return signEdgeGrid(req, auth) }}, nil
	}
	return base, nil
}

func requestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("request body cannot be replayed for authentication")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if len(data) > maxBody {
		return nil, errors.New("request body exceeds authentication size limit")
	}
	return data, err
}

func signEdgeGrid(req *http.Request, auth collection.Auth) error {
	clientToken, clientSecret, accessToken := authValue(auth, "clientToken"), authValue(auth, "clientSecret"), authValue(auth, "accessToken")
	if clientToken == "" || clientSecret == "" || accessToken == "" {
		return errors.New("EdgeGrid requires clientToken, clientSecret and accessToken")
	}
	nonce := authValue(auth, "nonce")
	if nonce == "" {
		data := make([]byte, 16)
		if _, err := rand.Read(data); err != nil {
			return err
		}
		data[6] = (data[6] & 0x0f) | 0x40
		data[8] = (data[8] & 0x3f) | 0x80
		nonce = fmt.Sprintf("%x-%x-%x-%x-%x", data[:4], data[4:6], data[6:8], data[8:10], data[10:])
	}
	timestamp := cmp.Or(authValue(auth, "timestamp"), time.Now().UTC().Format("20060102T15:04:05+0000"))
	header := "EG1-HMAC-SHA256 client_token=" + clientToken + ";access_token=" + accessToken + ";timestamp=" + timestamp + ";nonce=" + nonce + ";"
	var signedHeaders []string
	for _, name := range strings.Split(authValue(auth, "headersToSign"), ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			signedHeaders = append(signedHeaders, name)
		}
	}
	slices.Sort(signedHeaders)
	var normalized []string
	for _, name := range signedHeaders {
		normalized = append(normalized, name+":"+strings.Join(strings.Fields(req.Header.Get(name)), " "))
	}
	bodyHash := ""
	if req.Method == "POST" {
		body, err := requestBody(req)
		if err != nil {
			return err
		}
		limit := 131072
		if value := authValue(auth, "maxBodySize"); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number < 0 {
				return errors.New("invalid EdgeGrid maxBodySize")
			}
			limit = number
		}
		if len(body) > limit {
			body = body[:limit]
		}
		if len(body) > 0 {
			hash := sha256.Sum256(body)
			bodyHash = base64.StdEncoding.EncodeToString(hash[:])
		}
	}
	base := req.URL
	if raw := authValue(auth, "baseURL"); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return errors.New("invalid EdgeGrid base URL")
		}
		base = parsed
	}
	canonical := strings.Join([]string{req.Method, base.Scheme, base.Host, req.URL.RequestURI(), strings.Join(normalized, "\t"), bodyHash, header}, "\t")
	mac := hmac.New(sha256.New, []byte(clientSecret))
	io.WriteString(mac, timestamp)
	key := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	mac = hmac.New(sha256.New, []byte(key))
	io.WriteString(mac, canonical)
	req.Header.Set("Authorization", header+"signature="+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return nil
}
