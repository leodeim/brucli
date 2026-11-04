<p align="center">
  <img src="misc/logo.png" alt="brucli logo" width="180">
</p>

<h1 align="center">brucli</h1>

<p align="center">
  <strong>A terminal API client.</strong><br>
  Browse, edit and send requests, or run collections in CI.
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#update">Update</a> ·
  <a href="#keys">Keys</a> ·
  <a href="#compatibility">Compatibility</a> ·
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

brucli reads and writes OpenCollection YAML and `.bru` collections used by [Bruno](https://www.usebruno.com). It saves edits in the collection's original format.

## Install

Install the latest release on macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/leodeim/brucli/main/scripts/install.sh | sh
```

The script detects your OS and CPU, verifies the SHA-256 checksum, and installs to `~/.local/bin`. Set `INSTALL_DIR` to use another directory you can write to:

```sh
curl -fsSL https://raw.githubusercontent.com/leodeim/brucli/main/scripts/install.sh | INSTALL_DIR="$HOME/bin" sh
```

If `~/.local/bin` is missing from your `PATH`, add this to your shell profile:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

For a manual install, download a binary from the [latest release](https://github.com/leodeim/brucli/releases/latest):

| Platform | Binary |
| --- | --- |
| macOS, Apple Silicon | [brucli_darwin_arm64](https://github.com/leodeim/brucli/releases/latest/download/brucli_darwin_arm64) |
| macOS, Intel | [brucli_darwin_amd64](https://github.com/leodeim/brucli/releases/latest/download/brucli_darwin_amd64) |
| Linux, Intel/AMD | [brucli_linux_amd64](https://github.com/leodeim/brucli/releases/latest/download/brucli_linux_amd64) |
| Linux, ARM64 | [brucli_linux_arm64](https://github.com/leodeim/brucli/releases/latest/download/brucli_linux_arm64) |

Rename the downloaded file to `brucli`, run `chmod +x brucli`, and move it to a directory on your `PATH`. Each release includes a `checksums.txt` file with SHA-256 checksums.

Or install with Go 1.26.10 or newer:

```sh
go install github.com/leodeim/brucli@latest
```

Go installs to `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset. Add that directory to your `PATH`.

## Update

```sh
brucli update
```

brucli downloads the latest release for your OS and CPU, verifies its SHA-256 checksum, and replaces the installed binary. The install directory must be writable.

Release binaries and Go builds installed from version tags support updates. Development builds cannot update themselves. Run `brucli --version` to check your version.

## Run

```sh
brucli path/to/collection                              # open the UI
brucli -env local path/to/collection                   # start with an environment
brucli run -env staging -tags read path/to/collection  # run without the UI
```

Pass a directory containing `opencollection.yml` for a YAML collection or `bruno.json` for a `.bru` collection. Omit the path to use the current directory.

`brucli run` exits with code `1` when a request, assertion, test or script fails, and `2` for usage errors. It accepts `-env`, `-env-var name=value`, `-insecure`, `-tags`, `-exclude-tags` and `-folder`. Run `brucli run --help` for all flags.

In the UI, `-no-mouse` leaves the mouse to the terminal so you can select text.

## Keys

Press `?` to search actions and shortcuts, or `F10` to open the menus. You can also click and scroll.

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

In the body editor, `Esc` closes the editor and keeps your changes, `ctrl+f` formats JSON and `ctrl+z` undoes.

## Compatibility

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

Pre-request and post-response scripts support Bruno's `req`, `res` and `bru` objects, post-response variables, assertions, and `test()` with a chai-style `expect`. `require()` and `bru.sendRequest` are not implemented. gRPC lifecycle scripts stay in the file but do not run.

### gRPC and WebSocket

Choose gRPC or WebSocket from a request's Body type menu, or open an existing request. For gRPC, enter a method such as `/package.Service/Method` and an optional `.proto` path relative to the collection. brucli also reads collection protobuf settings. Without local proto files, it uses server reflection. Use `grpc://` for plaintext and `grpcs://` for TLS.

For WebSockets, `s` connects and sends the selected saved message. While connected, `s` opens a message composer and `Esc` disconnects. Binary messages use base64 data. Responses appear as they arrive.

Headless runs receive one WebSocket response by default. `-stream-messages N` changes the count; `0` waits for the server to close.

Headless gRPC and WebSocket streams time out after 60 seconds by default. Use `-stream-timeout 5m` to change this. A request's timeout takes precedence over the flag. Streams retain at most 64 MiB or 10,000 messages.

### Environments and connections

Choose a workspace environment from the Environment menu or pass `-global-env NAME`. brucli loads these environments from `environments/` beside the nearest `workspace.yml`, `workspace.yaml` or `workspace.json`. Collection and request variables take precedence over global variables. Secret values stay in memory and are not saved to environment files.

Collection proxy and certificate settings apply automatically. Use `-proxy URL` to override the proxy, `-noproxy` to bypass proxies, or `-cacert FILE` to add a trusted CA. `-client-cert-config FILE` reads [Bruno's certificate configuration JSON](https://docs.usebruno.com/bru-cli/proxyConfiguration). Certificate paths in collection settings are relative to the collection; paths in the CLI configuration are relative to that file.

OAuth 2 supports client credentials, password credentials, authorization code with PKCE, and implicit flows. Browser login requires an HTTP callback on localhost or a loopback IP. For browser-based flows in headless runs, supply an access token or, for authorization-code flows, an authorization code. brucli caches tokens for the session and can renew expired tokens when a refresh token is available.

## Development

```sh
make all   # gofmt, vet, tests, build
```

GitHub Actions checks formatting, runs vet, and tests with the race detector on macOS and Linux. After the checks pass, each push to `main` publishes the next patch release with binaries for both platforms on amd64 and arm64. Pushing a `v*` tag publishes that version instead.

## Support

If brucli is useful to you, you can [buy me a coffee](https://buymeacoffee.com/leodeim).
