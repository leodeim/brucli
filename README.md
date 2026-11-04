<h1 align="center">brucli</h1>

<p align="center">
  <strong>A terminal client for Bruno collections.</strong><br>
  Browse, edit and send the requests of a Bruno YAML collection, or run them headless in CI.
</p>

<p align="center">
  <a href="#install">Install</a> ·
  <a href="#keys">Keys</a> ·
  <a href="#bruno-compatibility">Bruno compatibility</a>
</p>

<p align="center">
  <a href="https://github.com/leodeim/brucli/blob/main/LICENSE"><img src="https://img.shields.io/github/license/leodeim/brucli" alt="License"></a>
</p>

brucli reads and writes the same files as the [Bruno](https://www.usebruno.com) app (the YAML/OpenCollection format), so one collection works in both. brucli saves in Bruno's layout and quoting; an unchanged request comes out byte for byte the same.

## Install

Requires Go 1.26 or newer.

```sh
go install github.com/leodeim/brucli@latest
```

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
