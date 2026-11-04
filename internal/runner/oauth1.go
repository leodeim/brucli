package runner

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leodeim/brucli/internal/collection"
)

func oauthEscape(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }

func (s *Session) signOAuth1(req *http.Request, auth collection.Auth) error {
	consumerKey := authValue(auth, "consumerKey")
	if consumerKey == "" {
		return errors.New("OAuth 1 requires a consumerKey")
	}
	method := authValue(auth, "signatureMethod")
	if method == "" {
		method = "HMAC-SHA1"
	}
	var algorithm crypto.Hash
	var newHash func() hash.Hash
	switch strings.TrimPrefix(strings.TrimPrefix(method, "HMAC-"), "RSA-") {
	case "SHA1":
		algorithm, newHash = crypto.SHA1, sha1.New
	case "SHA256":
		algorithm, newHash = crypto.SHA256, sha256.New
	case "SHA512":
		algorithm, newHash = crypto.SHA512, sha512.New
	case "PLAINTEXT":
	default:
		return fmt.Errorf("unsupported OAuth 1 signature method %q", method)
	}
	nonce := authValue(auth, "nonce")
	if nonce == "" {
		data := make([]byte, 16)
		if _, err := rand.Read(data); err != nil {
			return err
		}
		nonce = hex.EncodeToString(data)
	}
	timestamp := authValue(auth, "timestamp")
	if timestamp == "" {
		timestamp = strconv.FormatInt(time.Now().Unix(), 10)
	}
	parameters := url.Values{
		"oauth_consumer_key": {consumerKey}, "oauth_nonce": {nonce}, "oauth_timestamp": {timestamp}, "oauth_signature_method": {method},
	}
	version := authValue(auth, "version")
	if version == "" {
		version = "1.0"
	}
	parameters.Set("oauth_version", version)
	for _, pair := range []struct{ key, option string }{{"oauth_token", "accessToken"}, {"oauth_callback", "callbackUrl"}, {"oauth_verifier", "verifier"}} {
		if value := authValue(auth, pair.option); value != "" {
			parameters.Set(pair.key, value)
		}
	}
	body, err := requestBody(req)
	if err != nil {
		return err
	}
	if authValue(auth, "includeBodyHash") == "true" {
		h := sha1.New()
		if newHash != nil {
			h = newHash()
		}
		h.Write(body)
		parameters.Set("oauth_body_hash", base64.StdEncoding.EncodeToString(h.Sum(nil)))
	}
	all := req.URL.Query()
	if strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		fields, err := url.ParseQuery(string(body))
		if err != nil {
			return err
		}
		for key, values := range fields {
			for _, value := range values {
				all.Add(key, value)
			}
		}
	}
	for key, values := range parameters {
		all.Del(key)
		for _, value := range values {
			all.Add(key, value)
		}
	}
	all.Del("oauth_signature")
	type pair struct{ key, value string }
	var pairs []pair
	for key, values := range all {
		for _, value := range values {
			pairs = append(pairs, pair{oauthEscape(key), oauthEscape(value)})
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if result := strings.Compare(a.key, b.key); result != 0 {
			return result
		}
		return strings.Compare(a.value, b.value)
	})
	var normalized []string
	for _, pair := range pairs {
		normalized = append(normalized, pair.key+"="+pair.value)
	}
	baseURL := *req.URL
	baseURL.Scheme, baseURL.Host = strings.ToLower(baseURL.Scheme), strings.ToLower(baseURL.Host)
	baseURL.RawQuery, baseURL.Fragment, baseURL.User = "", "", nil
	if baseURL.Path == "" {
		baseURL.Path = "/"
	}
	if baseURL.Scheme == "http" && baseURL.Port() == "80" || baseURL.Scheme == "https" && baseURL.Port() == "443" {
		baseURL.Host = baseURL.Hostname()
	}
	base := req.Method + "&" + oauthEscape(baseURL.String()) + "&" + oauthEscape(strings.Join(normalized, "&"))
	key := oauthEscape(authValue(auth, "consumerSecret")) + "&" + oauthEscape(authValue(auth, "accessTokenSecret"))
	signature := ""
	switch {
	case method == "PLAINTEXT":
		signature = key
	case strings.HasPrefix(method, "HMAC-"):
		mac := hmac.New(newHash, []byte(key))
		io.WriteString(mac, base)
		signature = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	case strings.HasPrefix(method, "RSA-"):
		text := authValue(auth, "privateKey")
		if authValue(auth, "privateKeyType") == "file" {
			path := text
			if !filepath.IsAbs(path) {
				path = filepath.Join(s.Collection.Dir, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text = string(data)
		}
		block, _ := pem.Decode([]byte(text))
		if block == nil {
			return errors.New("invalid OAuth 1 RSA private key")
		}
		var private *rsa.PrivateKey
		if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			private, _ = parsed.(*rsa.PrivateKey)
		} else {
			private, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
		}
		if private == nil {
			return errors.New("OAuth 1 private key must be an RSA key in PKCS#1 or PKCS#8 PEM format")
		}
		h := newHash()
		io.WriteString(h, base)
		signed, err := rsa.SignPKCS1v15(rand.Reader, private, algorithm, h.Sum(nil))
		if err != nil {
			return err
		}
		signature = base64.StdEncoding.EncodeToString(signed)
	}
	parameters.Set("oauth_signature", signature)
	switch placement := authValue(auth, "placement"); placement {
	case "query", "queryparams":
		query := req.URL.Query()
		for key, values := range parameters {
			query[key] = values
		}
		req.URL.RawQuery = query.Encode()
	case "body":
		if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			return errors.New("OAuth 1 body placement requires a form-urlencoded body")
		}
		fields, err := url.ParseQuery(string(body))
		if err != nil {
			return err
		}
		for key, values := range parameters {
			fields[key] = values
		}
		data := fields.Encode()
		req.Body = io.NopCloser(strings.NewReader(data))
		req.ContentLength = int64(len(data))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(data)), nil }
	case "", "header":
		var fields []string
		if realm := authValue(auth, "realm"); realm != "" {
			fields = append(fields, `realm="`+oauthEscape(realm)+`"`)
		}
		keys := make([]string, 0, len(parameters))
		for key := range parameters {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			fields = append(fields, oauthEscape(key)+`="`+oauthEscape(parameters.Get(key))+`"`)
		}
		req.Header.Set("Authorization", "OAuth "+strings.Join(fields, ", "))
	default:
		return fmt.Errorf("unsupported OAuth 1 placement %q", placement)
	}
	return nil
}
