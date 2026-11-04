<p align="center">
  <img src="misc/logo.png" alt="brucli logo" width="180">
</p>

<h1 align="center">brucli</h1>

<p align="center">
  <strong>A terminal client for Bruno collections.</strong><br>
  Browse, edit and send requests from Bruno collections, or run them headless in CI.
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#update">Update</a> ·
  <a href="#keys">Keys</a> ·
  <a href="#bruno-compatibility">Bruno compatibility</a> ·
  <a href="#support">Support</a>
</p>

<p align="center">
  <a href="https://github.com/leodeim/brucli/actions/workflows/release.yml"><img src="https://github.com/leodeim/brucli/actions/workflows/release.yml/badge.svg" alt="Build and Release"></a>
  <a href="https://github.com/leodeim/brucli/releases/latest"><img src="https://img.shields.io/github/v/release/leodeim/brucli" alt="Latest Release"></a>
  <a href="https://github.com/leodeim/brucli/releases"><img src="https://img.shields.io/github/downloads/leodeim/brucli/total" alt="Downloads"></a>
  <a href="https://github.com/leodeim/brucli/blob/main/LICENSE"><img src="https://img.shields.io/github/license/leodeim/brucli" alt="License"></a>
</p>

<p align="center">
  <a href="https://buymeacoffee.com/leodeim"><img src="https://img.shields.io/badge/Buy_Me_a_Coffee-Support_My_Work-yellow?logo=buymeacoffee" alt="Buy me a coffee"></a>
</p>

brucli reads and writes [Bruno](https://www.usebruno.com) collections in both OpenCollection YAML (`opencollection.yml`) and `.bru` (`bruno.json`) formats. Edits stay in the collection's original format, and unrecognized blocks stay in the file.

## Install

Install the latest release on macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/leodeim/brucli/main/scripts/install.sh | sh
```

The script detects your OS and CPU, verifies the SHA-256 checksum, and installs to `~/.local/bin`. To choose another directory:

```sh
curl -fsSL https://raw.githubusercontent.com/leodeim/brucli/main/scripts/install.sh | INSTALL_DIR=/usr/local/bin sh
```

The directory must be writable. If `~/.local/bin` is missing from your `PATH`, add this to your shell profile:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Download a binary from the [latest release](https://github.com/leodeim/brucli/releases/latest):

| Platform | Binary |
| --- | --- |
| macOS, Apple Silicon | [brucli_darwin_arm64](https://github.com/leodeim/brucli/releases/latest/download/brucli_darwin_arm64) |
| macOS, Intel | [brucli_darwin_amd64](https://github.com/leodeim/brucli/releases/latest/download/brucli_darwin_amd64) |
| Linux, Intel/AMD | [brucli_linux_amd64](https://github.com/leodeim/brucli/releases/latest/download/brucli_linux_amd64) |
| Linux, ARM64 | [brucli_linux_arm64](https://github.com/leodeim/brucli/releases/latest/download/brucli_linux_arm64) |

Rename the downloaded file to `brucli`, run `chmod +x brucli`, and move it to a directory on your `PATH`. Each release includes a `checksums.txt` file with SHA-256 checksums.

You can also install with Go 1.26.10 or newer:

```sh
go install github.com/leodeim/brucli@latest
```

Go installs to `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset. Add that directory to your `PATH`.

## Update

```sh
brucli update
```

Downloads the latest release for your OS and CPU, verifies its SHA-256 checksum, and replaces the installed binary. The install directory must be writable. Release binaries and Go installs from tagged releases support updates; development builds do not. Run `brucli --version` to check your version.

## Run

```sh
brucli path/to/collection                              # open the UI
brucli -env local path/to/collection                   # start with an environment
brucli run -env staging -tags read path/to/collection  # send without the UI, like bru run
```

`brucli run` exits 1 when a request, assertion, test or script fails, and 2 on usage errors. It takes `-env`, `-env-var name=value`, `-insecure`, `-tags`, `-exclude-tags` and `-folder`. In the UI, `-no-mouse` leaves the mouse to the terminal so you can select text.

## Keys

Press `?` to search every action and its key, or `F10` for the menus. You can also click and scroll.

| Key | Action |
| --- | --- |
| `s` / `ctrl+r` | Send the request |
| `s` while a WebSocket is connected | Compose and send another message |
| `ctrl+s` | Save |
| `Tab` | Switch pane |
| `Enter` | Open a request, or edit the selected row |
| `b` | Edit the body |
| `u` / `m` | Edit the URL / pick the method |
| `t` in the Body tab | Pick the body type or switch to GraphQL, gRPC or WebSocket |
| `e` / `E` | Pick / edit the environment |
| `R` | Run a folder |
| `z` | Zoom the pane |
| `/` | Filter requests, or search the response |
| `c` / `y` | Copy as curl / copy the response body |
| `q` | Quit |

In the body editor, `Esc` leaves and keeps your changes, `ctrl+f` formats JSON and `ctrl+z` undoes.

## Bruno compatibility

| Feature | Support |
| --- | --- |
| Collections | Read, edit and run YAML and `.bru` collections, including folders and environments |
| HTTP | JSON, text, XML, SPARQL, URL-encoded forms and multipart uploads |
| GraphQL | GET and POST requests with query, variables and operation name |
| gRPC | Unary, client streaming, server streaming and bidirectional streaming; server reflection or local `.proto` files |
| WebSocket | Live connections, saved message variants, text, JSON and binary frames |
| Variables | Global, collection, environment, folder, request and runtime scopes, `.env`, and dynamic values such as `{{$guid}}` |
| Proxies | Collection settings, proxy environment variables, HTTP, HTTPS, SOCKS4 and SOCKS5 |
| Client certificates | PEM and PKCS#12, encrypted private keys, domain matching and custom CAs |

HTTP authentication supports basic, bearer, API key, Digest, NTLM, OAuth 1, OAuth 2, AWS SigV4, WSSE and Akamai EdgeGrid. Headers and auth inherit from the collection and folders. gRPC supports basic, bearer, API keys in metadata, and OAuth 2.

Existing scripting support includes pre-request and post-response scripts with Bruno's `req`, `res` and `bru` objects, post-response variables, assertions, and `test()` with a chai-style `expect`. `require()` and `bru.sendRequest` are not implemented. gRPC lifecycle scripts are kept in the file but are not executed.

### gRPC and WebSocket

Create a request and choose gRPC or WebSocket from the Body type menu, or open an existing request. For gRPC, enter the method (for example, `/package.Service/Method`) and an optional collection-relative `.proto` path. Collection protobuf settings are also read; without local proto files, brucli uses server reflection. Use `grpc://` for plaintext and `grpcs://` for TLS.

For WebSockets, `s` connects and sends the selected saved message. While connected, `s` opens a message composer and `Esc` disconnects. Binary messages use base64 data. Responses appear as they arrive.

Headless runs receive one WebSocket response by default. `-stream-messages N` changes the count; `0` waits for the server to close. Streams default to a 60-second timeout in headless runs. Set `-stream-timeout 5m` to change it, or set the request's timeout. Retained responses are limited to 64 MiB or 10,000 messages.

### Environments and connections

Choose a workspace environment from the Environment menu or pass `-global-env NAME`. brucli finds workspace environments beside the nearest parent `workspace.yml`, `workspace.yaml` or `workspace.json`. Collection and request variables take precedence over global variables. Both environment editors keep secret values in memory.

Collection proxy and certificate settings apply automatically. Use `-proxy URL` to override the proxy, `-noproxy` to bypass proxies, or `-cacert FILE` to add a trusted CA. `-client-cert-config FILE` reads [Bruno's certificate configuration JSON](https://docs.usebruno.com/bru-cli/proxyConfiguration). Certificate paths in collection settings are relative to the collection; paths in the CLI configuration are relative to that file.

OAuth 2 supports client credentials, password credentials, authorization code with PKCE, and implicit flows. Browser login requires an HTTP callback on localhost or a loopback IP. Headless browser flows need a supplied access token; authorization-code flows also accept a supplied code. Tokens are cached and refreshed within the session.

## Development

```sh
make all   # gofmt, vet, tests, build
```

GitHub Actions checks formatting, runs vet, and tests with the race detector on macOS and Linux. Each push to `main` publishes the next patch release with binaries for both platforms on amd64 and arm64. Pushing a `v*` tag publishes that version instead.

## Support

If brucli is useful to you, you can [buy me a coffee](https://buymeacoffee.com/leodeim).
