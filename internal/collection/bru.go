package collection

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type bruBlock struct {
	name, body string
	start, end int
}

type bruPair struct {
	name, value, description, raw, prefix string
	enabled                               bool
}

var bruStart = regexp.MustCompile(`(?m)^([a-zA-Z0-9:_-]+)[ \t]*([\{\[])[ \t]*\r?\n`)
var bruEnd = regexp.MustCompile(`(?m)^\}[ \t]*(?:\r?\n|$)`)
var bruListEnd = regexp.MustCompile(`(?m)^\][ \t]*(?:\r?\n|$)`)
var bruAnnotation = regexp.MustCompile(`^@[a-zA-Z0-9_-]+(?:\(.*\))?$`)

func parseBru(data string) ([]bruBlock, error) {
	var blocks []bruBlock
	position := 0
	for position < len(data) {
		match := bruStart.FindStringSubmatchIndex(data[position:])
		if match == nil {
			break
		}
		start := position + match[0]
		bodyStart := position + match[1]
		end := bruEnd.FindStringIndex(data[bodyStart:])
		if data[position+match[4]:position+match[5]] == "[" {
			end = bruListEnd.FindStringIndex(data[bodyStart:])
		}
		if end == nil {
			return nil, fmt.Errorf("unclosed .bru block at byte %d", start)
		}
		blocks = append(blocks, bruBlock{name: data[position+match[2] : position+match[3]], body: data[bodyStart : bodyStart+end[0]], start: start, end: bodyStart + end[1]})
		position = bodyStart + end[1]
	}
	return blocks, nil
}

func bruText(body string) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n")
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent > 0 {
		for i, line := range lines {
			if len(line) >= indent {
				lines[i] = line[indent:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

func bruPairs(body string) ([]bruPair, error) {
	lines := strings.SplitAfter(body, "\n")
	var pairs []bruPair
	prefix, description := "", ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "//") || bruAnnotation.MatchString(line) {
			prefix += lines[i]
			if strings.HasPrefix(line, "@description(") && strings.HasSuffix(line, ")") {
				description = strings.TrimSuffix(strings.TrimPrefix(line, "@description("), ")")
				if value, err := strconv.Unquote(description); err == nil {
					description = value
				}
			}
			continue
		}
		disabled := strings.HasPrefix(line, "~")
		line = strings.TrimPrefix(line, "~")
		colon := -1
		quoted, escaped := false, false
		for j, char := range line {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' && quoted {
				escaped = true
				continue
			}
			if char == '"' {
				quoted = !quoted
				continue
			}
			if char == ':' && !quoted {
				colon = j
				break
			}
		}
		if colon < 0 {
			return nil, fmt.Errorf("invalid .bru key/value %q", line)
		}
		name, value := strings.TrimSpace(line[:colon]), strings.TrimSpace(line[colon+1:])
		if strings.HasPrefix(name, "\"") {
			parsed, err := strconv.Unquote(name)
			if err != nil {
				return nil, err
			}
			name = parsed
		}
		raw := lines[i]
		if strings.HasPrefix(value, "'''") {
			value = strings.TrimPrefix(value, "'''")
			for !strings.Contains(value, "'''") && i+1 < len(lines) {
				i++
				raw += lines[i]
				value += "\n" + strings.TrimRight(lines[i], "\r\n")
			}
			end := strings.Index(value, "'''")
			if end < 0 {
				return nil, fmt.Errorf("unclosed multiline value for %s", name)
			}
			value = value[:end]
			if last := strings.LastIndex(value, "\n"); last >= 0 && strings.TrimSpace(value[last+1:]) == "" {
				value = value[:last]
			}
			value = bruText(strings.TrimPrefix(value, "\n"))
		} else if value == "[" {
			var values []string
			closed := false
			for i+1 < len(lines) {
				i++
				raw += lines[i]
				item := strings.TrimSpace(lines[i])
				if item == "]" {
					closed = true
					break
				}
				if item != "" {
					values = append(values, strings.TrimSuffix(item, ","))
				}
			}
			if !closed {
				return nil, fmt.Errorf("unclosed list for %s", name)
			}
			value = strings.Join(values, "\n")
		}
		pairs = append(pairs, bruPair{name: name, value: value, description: description, enabled: !disabled, raw: prefix + raw, prefix: prefix})
		prefix, description = "", ""
	}
	return pairs, nil
}

func bruValue(pairs []bruPair, name string) string {
	for _, pair := range pairs {
		if pair.name == name {
			return pair.value
		}
	}
	return ""
}

func bruKV(pairs []bruPair) []KV {
	var out []KV
	for _, pair := range pairs {
		out = append(out, KV{Name: pair.name, Value: pair.value, Description: pair.description, Enabled: pair.enabled})
	}
	return out
}

func bruAuth(mode string, blocks []bruBlock) *yaml.Node {
	if mode == "" || mode == "none" {
		return nil
	}
	if mode == "inherit" {
		return scalar(mode)
	}
	node := mapping()
	add(node, "type", scalar(mode))
	for _, block := range blocks {
		if block.name != "auth:"+mode {
			continue
		}
		pairs, _ := bruPairs(block.body)
		for _, pair := range pairs {
			add(node, pair.name, scalar(pair.value))
		}
	}
	return node
}

func bruNode(path string, data []byte) (*yaml.Node, error) {
	blocks, err := parseBru(string(data))
	if err != nil {
		return nil, err
	}
	if filepath.Base(filepath.Dir(path)) == envDir {
		return bruEnvironmentNode(path, blocks)
	}
	root, info, request, runtime := mapping(), mapping(), mapping(), mapping()
	add(root, "info", info)
	kind, method, mode, auth := "http", "GET", "none", "none"
	if filepath.Base(path) == "collection.bru" {
		kind = "collection"
	}
	if filepath.Base(path) == "folder.bru" {
		kind = "folder"
		auth = "inherit"
	}
	seenMeta := false
	for _, block := range blocks {
		if slices.Contains([]string{"get", "post", "put", "delete", "patch", "head", "options", "connect", "trace", "http", "grpc", "ws"}, block.name) {
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			mode = cmp.Or(bruValue(pairs, "body"), "none")
		}
	}
	var parameters []Param
	var scripts Scripts
	var assertions []Assertion
	var actions []Action
	var body Body
	var messages []Message
	for _, block := range blocks {
		switch block.name {
		case "meta":
			seenMeta = true
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			kind = cmp.Or(bruValue(pairs, "type"), kind)
			if kind == "ws" {
				kind = "websocket"
			}
			add(info, "name", scalar(bruValue(pairs, "name")))
			if seq := bruValue(pairs, "seq"); seq != "" {
				number, _ := strconv.Atoi(seq)
				add(info, "seq", integer(number))
			}
			if tags := bruValue(pairs, "tags"); tags != "" {
				var items []*yaml.Node
				for _, tag := range strings.Split(tags, "\n") {
					items = append(items, scalar(tag))
				}
				add(info, "tags", sequence(items))
			}
		case "get", "post", "put", "delete", "patch", "head", "options", "connect", "trace", "http", "grpc", "ws":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			method = strings.ToUpper(block.name)
			if block.name == "http" || block.name == "grpc" {
				method = bruValue(pairs, "method")
			}
			add(request, "url", scalar(bruValue(pairs, "url")))
			mode, auth = cmp.Or(bruValue(pairs, "body"), "none"), cmp.Or(bruValue(pairs, "auth"), "none")
			for _, key := range []string{"methodType", "protoPath"} {
				if value := bruValue(pairs, key); value != "" {
					target := key
					if key == "protoPath" {
						target = "protoFilePath"
					}
					add(request, target, scalar(value))
				}
			}
		case "headers", "metadata":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			key := block.name
			add(request, key, kvsNode(bruKV(pairs)))
		case "params:query", "query", "params:path":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			for _, kv := range bruKV(pairs) {
				typ := "query"
				if block.name == "params:path" {
					typ = "path"
				}
				parameters = append(parameters, Param{KV: kv, Type: typ})
			}
		case "auth":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			auth = cmp.Or(bruValue(pairs, "mode"), "none")
		case "body:json", "body:text", "body:xml", "body:sparql", "body:graphql":
			if strings.TrimPrefix(block.name, "body:") == mode {
				body.Data = bruText(block.body)
			}
		case "body:graphql:vars":
			body.Variables = bruText(block.body)
		case "body:grpc", "body:ws":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			messages = append(messages, Message{Title: cmp.Or(bruValue(pairs, "name"), fmt.Sprintf("message %d", len(messages)+1)), Type: cmp.Or(bruValue(pairs, "type"), "json"), Data: bruValue(pairs, "content"), Selected: bruValue(pairs, "selected") == "true"})
		case "body:form-urlencoded", "body:multipart-form":
			if strings.TrimPrefix(block.name, "body:") != mode {
				continue
			}
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			body.Form = bruKV(pairs)
			if mode == "multipart-form" {
				for i := range body.Form {
					field := &body.Form[i]
					matches := bruFiles.FindAllStringSubmatch(field.Value, -1)
					if len(matches) > 0 {
						var files []string
						for _, match := range matches {
							files = append(files, match[1])
						}
						field.Type = "file"
						field.Value = strings.Join(files, "\n")
					} else {
						field.Type = "text"
					}
					if match := bruContentType.FindStringSubmatch(pairs[i].value); match != nil {
						field.ContentType = match[1]
						if field.Type == "text" {
							field.Value = strings.TrimSpace(bruContentType.ReplaceAllString(field.Value, ""))
						}
					}
				}
			}
		case "vars:pre-request":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			add(runtime, "variables", kvsNode(bruKV(pairs)))
		case "vars:post-response":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			for _, pair := range pairs {
				scope := "runtime"
				name := pair.name
				if strings.HasPrefix(name, "@") {
					scope = "request"
					name = strings.TrimPrefix(name, "@")
				}
				actions = append(actions, Action{Name: name, Scope: scope, Expression: strings.TrimPrefix(pair.value, "$"), Description: pair.description, Enabled: pair.enabled})
			}
		case "assert":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			for _, pair := range pairs {
				op, value := "eq", pair.value
				if candidate, rest, found := strings.Cut(pair.value, " "); found && slices.Contains([]string{"eq", "neq", "gt", "gte", "lt", "lte", "contains", "notContains", "matches", "notMatches", "length", "in", "notIn", "startsWith", "endsWith"}, candidate) {
					op, value = candidate, rest
				}
				assertions = append(assertions, Assertion{Expression: strings.TrimPrefix(pair.name, "$"), Operator: op, Value: value, Description: pair.description, Enabled: pair.enabled})
			}
		case "script:pre-request":
			scripts.Before = bruText(block.body)
		case "script:post-response":
			scripts.After = bruText(block.body)
		case "tests":
			scripts.Tests = bruText(block.body)
		case "docs":
			add(root, "docs", scalar(bruText(block.body)))
		case "settings":
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			settings := mapping()
			for _, pair := range pairs {
				switch pair.name {
				case "encodeUrl", "followRedirects":
					add(settings, pair.name, boolean(pair.value == "true"))
				case "timeout", "maxRedirects":
					number, err := strconv.Atoi(pair.value)
					if err != nil {
						return nil, err
					}
					add(settings, pair.name, integer(number))
				}
			}
			add(root, "settings", settings)
		}
	}
	if !seenMeta && kind != "folder" && kind != "collection" {
		return nil, errors.New(".bru request has no meta block")
	}
	add(info, "type", scalar(kind))
	add(request, "method", scalar(method))
	add(request, "params", paramsNode(parameters))
	add(request, "auth", bruAuth(auth, blocks))
	body.Mode = mode
	add(request, "body", bodyNode(body))
	if kind == "grpc" || kind == "websocket" {
		add(request, "message", messagesNode(messages, kind))
	}
	add(runtime, "scripts", scriptsNode(scripts))
	add(runtime, "assertions", assertionsNode(assertions))
	add(runtime, "actions", actionsNode(actions))
	if kind == "collection" || kind == "folder" {
		add(root, "request", request)
		if variables := lookup(runtime, "variables"); variables >= 0 {
			add(request, "variables", runtime.Content[variables+1])
		}
		add(request, "scripts", scriptsNode(scripts))
	} else {
		add(root, kind, request)
		add(root, "runtime", runtime)
	}
	return root, nil
}

var bruFiles = regexp.MustCompile(`@file\(([^)]*)\)`)
var bruContentType = regexp.MustCompile(`@contentType\(([^)]*)\)`)

func bruEnvironmentNode(path string, blocks []bruBlock) (*yaml.Node, error) {
	node := mapping()
	add(node, "name", scalar(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))))
	var variables []*yaml.Node
	for _, block := range blocks {
		if block.name == "vars" {
			pairs, err := bruPairs(block.body)
			if err != nil {
				return nil, err
			}
			for _, kv := range bruKV(pairs) {
				entry := mapping()
				add(entry, "name", scalar(kv.Name))
				add(entry, "value", scalar(kv.Value))
				describe(entry, kv)
				variables = append(variables, entry)
			}
		}
		if block.name == "vars:secret" {
			for _, name := range strings.FieldsFunc(block.body, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
				name = strings.TrimSpace(name)
				if name == "" || strings.HasPrefix(name, "@") {
					continue
				}
				entry := mapping()
				add(entry, "name", scalar(strings.TrimPrefix(name, "~")))
				add(entry, "secret", boolean(true))
				if strings.HasPrefix(name, "~") {
					add(entry, "disabled", boolean(true))
				}
				variables = append(variables, entry)
			}
		}
	}
	add(node, "variables", sequence(variables))
	return node, nil
}

func readBruCollection(dir string) (rawCollection, error) {
	var raw rawCollection
	doc, err := readYAML(filepath.Join(dir, "bruno.json"))
	if err != nil {
		return raw, err
	}
	var legacy struct {
		Name               string   `yaml:"name"`
		Ignore             []string `yaml:"ignore"`
		Proxy              Proxy    `yaml:"proxy"`
		ClientCertificates struct {
			Enabled *bool               `yaml:"enabled"`
			Certs   []ClientCertificate `yaml:"certs"`
		} `yaml:"clientCertificates"`
	}
	if err := doc.Decode(&legacy); err != nil {
		return raw, err
	}
	raw.Info.Name = legacy.Name
	raw.Extensions.Bruno.Ignore = legacy.Ignore
	raw.Config.Proxy = legacy.Proxy
	if legacy.ClientCertificates.Enabled == nil || *legacy.ClientCertificates.Enabled {
		raw.Config.ClientCertificates = legacy.ClientCertificates.Certs
	}
	if i := lookup(doc, "protobuf"); i >= 0 {
		if err := doc.Content[i+1].Decode(&raw.Config.Protobuf); err != nil {
			return raw, err
		}
	}
	defaults, err := readYAML(filepath.Join(dir, "collection.bru"))
	if errors.Is(err, os.ErrNotExist) {
		return raw, nil
	}
	if err != nil {
		return raw, err
	}
	var collection rawCollection
	if err := defaults.Decode(&collection); err != nil {
		return raw, err
	}
	raw.Request, raw.Docs = collection.Request, collection.Docs
	return raw, nil
}
