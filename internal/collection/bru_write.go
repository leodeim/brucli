package collection

import (
	"cmp"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
)

func bruDictionary(pairs []bruPair) string {
	var text strings.Builder
	for _, pair := range pairs {
		if pair.description != "" {
			text.WriteString("  @description(" + strconv.Quote(pair.description) + ")\n")
		}
		name := pair.name
		if strings.ContainsAny(name, " :{}\t\r\n\"\\") || strings.HasPrefix(name, "~") {
			name = strconv.Quote(name)
		}
		if !pair.enabled {
			name = "~" + name
		}
		if strings.Contains(pair.value, "\n") || pair.name == "tags" {
			if pair.name == "tags" {
				text.WriteString("  " + name + ": [\n")
				for _, value := range strings.Split(pair.value, "\n") {
					text.WriteString("    " + value + "\n")
				}
				text.WriteString("  ]\n")
			} else {
				text.WriteString("  " + name + ": '''\n")
				for _, value := range strings.Split(pair.value, "\n") {
					text.WriteString("    " + value + "\n")
				}
				text.WriteString("  '''\n")
			}
		} else {
			text.WriteString("  " + name + ": " + pair.value + "\n")
		}
	}
	return text.String()
}

func bruRawText(value string) string {
	if value == "" {
		return ""
	}
	return "  " + strings.ReplaceAll(strings.TrimRight(value, "\n"), "\n", "\n  ") + "\n"
}

func bruFields(kvs []KV) string {
	var pairs []bruPair
	for _, kv := range kvs {
		value := kv.Value
		if kv.Type == "file" {
			var files []string
			for _, path := range strings.Split(value, "\n") {
				if path != "" {
					files = append(files, "@file("+path+")")
				}
			}
			value = strings.Join(files, " ")
		}
		if kv.ContentType != "" {
			value += " @contentType(" + kv.ContentType + ")"
		}
		pairs = append(pairs, bruPair{name: kv.Name, value: value, description: kv.Description, enabled: kv.Enabled})
	}
	return bruDictionary(pairs)
}

func (r *Request) bruBlocks() map[string]string {
	blocks := map[string]string{}
	meta := []bruPair{{name: "name", value: r.Name, enabled: true}, {name: "type", value: cmp.Or(r.Kind, "http"), enabled: true}, {name: "seq", value: strconv.Itoa(r.Seq), enabled: true}}
	if r.Kind == "websocket" {
		meta[1].value = "ws"
	}
	if len(r.Tags) > 0 {
		meta = append(meta, bruPair{name: "tags", value: strings.Join(r.Tags, "\n"), enabled: true})
	}
	blocks["meta"] = bruDictionary(meta)
	method := strings.ToLower(r.Method)
	pairs := []bruPair{{name: "url", value: r.URL, enabled: true}, {name: "body", value: cmp.Or(r.Body.Mode, "none"), enabled: true}, {name: "auth", value: cmp.Or(r.Auth.Mode, "none"), enabled: true}}
	if !slices.Contains([]string{"get", "post", "put", "patch", "delete", "options", "head", "trace", "connect"}, method) {
		method = "http"
		pairs = append(pairs, bruPair{name: "method", value: r.Method, enabled: true})
	}
	if r.Kind == "grpc" {
		method = "grpc"
		pairs = append(pairs, bruPair{name: "methodType", value: r.MethodType, enabled: true}, bruPair{name: "protoPath", value: r.ProtoFilePath, enabled: true})
	}
	if r.Kind == "websocket" {
		method = "ws"
		pairs[1].value = "ws"
	}
	blocks[method] = bruDictionary(pairs)
	headerBlock := "headers"
	if r.Kind == "grpc" {
		headerBlock = "metadata"
	}
	blocks[headerBlock] = bruFields(r.Headers)
	var query, path []KV
	for _, parameter := range r.Params {
		if parameter.Type == "path" {
			path = append(path, parameter.KV)
		} else {
			query = append(query, parameter.KV)
		}
	}
	blocks["params:query"], blocks["params:path"] = bruFields(query), bruFields(path)
	if r.Auth.Mode != "" && r.Auth.Mode != "none" && r.Auth.Mode != "inherit" {
		node := authNode(r.Auth)
		var pairs []bruPair
		if node != nil {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value != "type" {
					pairs = append(pairs, bruPair{name: node.Content[i].Value, value: node.Content[i+1].Value, enabled: true})
				}
			}
		}
		if r.Auth.Mode == "oauth2" {
			pairs = nil
			for _, field := range []struct {
				key   string
				names []string
			}{
				{"grant_type", []string{"flow", "grantType"}}, {"access_token_url", []string{"accessTokenUrl"}}, {"refresh_token_url", []string{"refreshTokenUrl"}},
				{"client_id", []string{"credentials.clientId", "clientId"}}, {"client_secret", []string{"credentials.clientSecret", "clientSecret"}}, {"scope", []string{"scope"}},
				{"username", []string{"resourceOwner.username", "username"}}, {"password", []string{"resourceOwner.password", "password"}},
				{"callback_url", []string{"callbackUrl"}}, {"authorization_url", []string{"authorizationUrl"}},
				{"state", []string{"state"}}, {"token_query_key", []string{"tokenConfig.placement.query", "tokenQueryKey"}}, {"token_header_prefix", []string{"tokenConfig.placement.header", "tokenHeaderPrefix"}},
				{"credentials_id", []string{"tokenConfig.id", "credentialsId"}}, {"token_placement", []string{"tokenPlacement"}}, {"token_source", []string{"tokenSource"}}, {"access_token", []string{"accessToken"}},
				{"credentials_placement", []string{"credentials.placement", "credentialsPlacement"}}, {"auto_fetch_token", []string{"settings.autoFetchToken", "autoFetchToken"}}, {"auto_refresh_token", []string{"settings.autoRefreshToken", "autoRefreshToken"}},
			} {
				value := r.Auth.Option(field.names...)
				if field.key == "grant_type" && value == "resource_owner_password_credentials" {
					value = "password"
				}
				if value != "" {
					pairs = append(pairs, bruPair{name: field.key, value: value, enabled: true})
				}
			}
			if r.Auth.Option("pkce") == "true" || r.Auth.Option("pkce.method") == "S256" {
				pairs = append(pairs, bruPair{name: "pkce", value: "true", enabled: true})
			}
			if r.Auth.Option("tokenConfig.placement.query") != "" {
				pairs = append(pairs, bruPair{name: "token_placement", value: "query", enabled: true})
			}
		}
		blocks["auth:"+r.Auth.Mode] = bruDictionary(pairs)
	}
	if r.Body.Mode != "" && r.Body.Mode != "none" {
		if r.Body.Mode == "form-urlencoded" || r.Body.Mode == "multipart-form" {
			blocks["body:"+r.Body.Mode] = bruFields(r.Body.Form)
		} else {
			blocks["body:"+r.Body.Mode] = bruRawText(r.Body.Data)
		}
		if r.Body.Mode == "graphql" {
			blocks["body:graphql:vars"] = bruRawText(r.Body.Variables)
		}
	}
	if r.Kind == "grpc" || r.Kind == "websocket" {
		delete(blocks, "body:"+r.Body.Mode)
		name := "body:grpc"
		if r.Kind == "websocket" {
			name = "body:ws"
		}
		var values []string
		for _, message := range r.Body.Messages {
			fields := []bruPair{{name: "name", value: message.Title, enabled: true}, {name: "content", value: message.Data, enabled: true}}
			if r.Kind == "websocket" {
				fields = append(fields, bruPair{name: "type", value: message.Type, enabled: true})
				if message.Selected {
					fields = append(fields, bruPair{name: "selected", value: "true", enabled: true})
				}
			}
			values = append(values, bruDictionary(fields))
		}
		blocks[name] = strings.Join(values, "}\n\n"+name+" {\n")
	}
	blocks["vars:pre-request"] = bruFields(r.Vars)
	var actions, assertions []bruPair
	for _, action := range r.Actions {
		name := action.Name
		if action.Scope == "request" {
			name = "@" + name
		}
		actions = append(actions, bruPair{name: name, value: action.Expression, description: action.Description, enabled: action.Enabled})
	}
	for _, assertion := range r.Assertions {
		value := assertion.Value
		if assertion.Operator != "" && assertion.Operator != "eq" {
			value = assertion.Operator + " " + value
		}
		assertions = append(assertions, bruPair{name: assertion.Expression, value: value, description: assertion.Description, enabled: assertion.Enabled})
	}
	blocks["vars:post-response"], blocks["assert"] = bruDictionary(actions), bruDictionary(assertions)
	blocks["script:pre-request"], blocks["script:post-response"], blocks["tests"] = bruRawText(r.Scripts.Before), bruRawText(r.Scripts.After), bruRawText(r.Scripts.Tests)
	blocks["docs"] = bruRawText(r.Docs)
	return blocks
}

func mergeBruDictionary(original, previous, next string) string {
	old, err := bruPairs(original)
	if err != nil {
		return next
	}
	wanted, err := bruPairs(next)
	if err != nil {
		return next
	}
	before, err := bruPairs(previous)
	if err != nil {
		return next
	}
	managed := map[string]bool{}
	for _, pair := range append(before, wanted...) {
		managed[pair.name] = true
	}
	used := make([]bool, len(old))
	var text strings.Builder
	for _, pair := range wanted {
		index := -1
		for i, source := range old {
			if !used[i] && source.name == pair.name {
				index = i
				break
			}
		}
		if index < 0 {
			text.WriteString(pair.raw)
			continue
		}
		used[index] = true
		source := old[index]
		if source.value == pair.value && source.enabled == pair.enabled && source.description == pair.description {
			text.WriteString(source.raw)
		} else if source.description == pair.description {
			text.WriteString(source.prefix + strings.TrimPrefix(pair.raw, pair.prefix))
		} else {
			text.WriteString(pair.raw)
		}
	}
	consumed := 0
	for _, source := range old {
		consumed += len(source.raw)
		if !managed[source.name] {
			text.WriteString(source.raw)
		}
	}
	if consumed < len(original) {
		text.WriteString(original[consumed:])
	}
	return text.String()
}

func writeBru(path, source string, previous, next map[string]string) error {
	blocks, err := parseBru(source)
	if err != nil {
		return err
	}
	var text strings.Builder
	written := map[string]bool{}
	position := 0
	for _, block := range blocks {
		text.WriteString(source[position:block.start])
		if written[block.name] && (block.name == "body:grpc" || block.name == "body:ws") && previous[block.name] != next[block.name] {
			position = block.end
			continue
		}
		name := block.name
		if _, exists := next[name]; !exists && slices.Contains([]string{"get", "post", "put", "patch", "delete", "options", "head", "trace", "connect", "http", "grpc", "ws"}, name) {
			for _, candidate := range []string{"get", "post", "put", "patch", "delete", "options", "head", "trace", "connect", "http", "grpc", "ws"} {
				if _, exists := next[candidate]; exists {
					name = candidate
					break
				}
			}
		}
		value, managed := next[name]
		before, wasManaged := previous[block.name]
		if !managed && !wasManaged || value == before && name == block.name {
			text.WriteString(source[block.start:block.end])
		} else if value != "" {
			if !strings.HasPrefix(block.name, "body:") || block.name == "body:form-urlencoded" || block.name == "body:multipart-form" {
				value = mergeBruDictionary(block.body, before, value)
			}
			if name == "vars:secret" {
				text.WriteString(name + " [\n" + value + "]\n")
			} else {
				text.WriteString(name + " {\n" + value + "}\n")
			}
		}
		written[name] = true
		written[block.name] = true
		position = block.end
	}
	text.WriteString(source[position:])
	keys := make([]string, 0, len(next))
	for name := range next {
		if !written[name] && next[name] != "" {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	// Meta must precede raw body blocks, and is easier to find at the top.
	if index := slices.Index(keys, "meta"); index >= 0 {
		keys = append([]string{"meta"}, slices.Delete(keys, index, index+1)...)
	}
	for _, name := range keys {
		if text.Len() > 0 {
			text.WriteString("\n")
		}
		if name == "vars:secret" {
			text.WriteString(name + " [\n" + next[name] + "]\n")
		} else {
			text.WriteString(name + " {\n" + next[name] + "}\n")
		}
	}
	return writeAtomic(path, []byte(text.String()))
}

func (r *Request) saveBru() error {
	source := r.bruSource
	if data, err := os.ReadFile(r.Path); err == nil {
		source = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous := map[string]string{}
	if source != "" {
		node, err := bruNode(r.Path, []byte(source))
		if err != nil {
			return err
		}
		old, err := requestFromNode(r.Path, node)
		if err != nil {
			return err
		}
		previous = old.bruBlocks()
	}
	if err := writeBru(r.Path, source, previous, r.bruBlocks()); err != nil {
		return err
	}
	data, err := os.ReadFile(r.Path)
	r.bruSource = string(data)
	return err
}

func environmentBruBlocks(e *Environment) map[string]string {
	var fields []KV
	var secrets strings.Builder
	for _, variable := range e.Vars {
		if variable.Secret {
			prefix := "  "
			if !variable.Enabled {
				prefix += "~"
			}
			secrets.WriteString(prefix + variable.Name + "\n")
		} else {
			fields = append(fields, variable.KV)
		}
	}
	return map[string]string{"vars": bruFields(fields), "vars:secret": secrets.String()}
}

func (e *Environment) saveBru() error {
	source := e.bruSource
	if data, err := os.ReadFile(e.Path); err == nil {
		source = string(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous := map[string]string{}
	if source != "" {
		old, err := readEnvironment(e.Path)
		if err != nil {
			return err
		}
		previous = environmentBruBlocks(old)
	}
	return writeBru(e.Path, source, previous, environmentBruBlocks(e))
}
