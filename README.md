<p align="center">
  <img src="misc/logo.png" alt="brucli logo" width="180">
</p>

<h1 align="center">brucli</h1>

<p align="center">
  <strong>A terminal client for Bruno collections.</strong><br>
  Browse, edit and send the requests of a Bruno YAML collection, or run them headless in CI.
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

brucli reads and writes the same files as the [Bruno](https://www.usebruno.com) app (the YAML/OpenCollection format), so one collection works in both. brucli saves in Bruno's layout and quoting; an unchanged request comes out byte for byte the same.

## Install

Download a binary from the [latest release](https://github.com/leodeim/brucli/releases/latest):

| Platform | Binary |
| --- | --- |
| macOS, Apple Silicon | [brucli_darwin_arm64](https://github.com/leodeim/brucli/releases/latest/download/brucli_darwin_arm64) |
| macOS, Intel | [brucli_darwin_amd64](https://github.com/leodeim/brucli/releases/latest/download/brucli_darwin_amd64) |
| Linux, Intel/AMD | [brucli_linux_amd64](https://github.com/leodeim/brucli/releases/latest/download/brucli_linux_amd64) |
| Linux, ARM64 | [brucli_linux_arm64](https://github.com/leodeim/brucli/releases/latest/download/brucli_linux_arm64) |

Rename the downloaded file to `brucli`, run `chmod +x brucli`, and move it to a directory on your `PATH`. Each release includes a `checksums.txt` file with SHA-256 checksums.

You can also install with Go 1.26 or newer:

```sh
go install github.com/leodeim/brucli@latest
```

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
| `ctrl+s` | Save |
| `Tab` | Switch pane |
| `Enter` | Open a request, or edit the selected row |
| `b` | Edit the body |
| `u` / `m` | Edit the URL / pick the method |
| `e` / `E` | Pick / edit the environment |
| `R` | Run a folder |
| `z` | Zoom the pane |
| `/` | Filter requests, or search the response |
| `c` / `y` | Copy as curl / copy the response body |
| `q` | Quit |

In the body editor, `Esc` leaves and keeps your changes, `ctrl+f` formats JSON and `ctrl+z` undoes.

## Bruno compatibility

brucli handles variables in every scope (including `process.env` from `.env` and dynamic values like `{{$guid}}`), inherited headers and auth (basic, bearer, API key), pre-request and post-response scripts with Bruno's `req`, `res` and `bru` objects, post-response variables, assertions, and `test()` with a chai-style `expect`.

It does not handle `.bru` collections, GraphQL, gRPC or WebSocket requests, other auth modes, multipart bodies, `require()` or `bru.sendRequest` in scripts, global environments, proxies or client certificates. Unsupported parts of a file stay as they are.

## Development

```sh
make all   # gofmt, vet, tests, build
```

GitHub Actions checks formatting, runs vet, and tests with the race detector on macOS and Linux. Each push to `main` publishes the next patch release with binaries for both platforms on amd64 and arm64. Pushing a `v*` tag publishes that version instead.

## Support

If brucli is useful to you, you can [buy me a coffee](https://buymeacoffee.com/leodeim).
