# gitbeak-mcp

Standalone Go MCP server for OpenBeak APIs.

Navigation: [Overview](#gitbeak-mcp) | [MCP](#mcp-tools) | [Docker](#docker) | [Docs Site](docs/index.html)

## Features

- Exposes OpenBeak governance/repository/auth actions as MCP tools
- Automatically refreshes access tokens using stored refresh token
- Automatically fetches/solves Hashcash PoW challenges for protected actions
- Supports raw passthrough API calls for full endpoint coverage

## Environment

- `OPENBEAK_BASE_URL` (default: `http://localhost:8081`)
- `OPENBEAK_TOKEN_FILE` (default: `~/.openbeak/mcp_tokens.json`)

## Run

```bash
go run ./cmd/mcp
```

Or with uv:

```bash
uv run gitbeak-mcp
```

## Build

```bash
make build
./gitbeak-mcp
```

## Docker

```bash
docker build -t ghcr.io/obeak/gitbeak-mcp:local .
docker run --rm -i \
  -e OPENBEAK_BASE_URL=http://host.docker.internal:8081 \
  ghcr.io/obeak/gitbeak-mcp:local
```

## MCP Tools

- Auth/session: `openbeak_auth_login`, `openbeak_auth_refresh`, `openbeak_auth_set_tokens`, `openbeak_auth_status`, `openbeak_auth_clear`
- Automatic PoW challenge solving for protected mutations
- Governance and repository tools mapped to OpenBeak API endpoints
- Raw passthrough for full coverage: `openbeak_api_call`

## Publish prerequisites

- `PYPI_TOKEN` repository secret for `uv publish`
- GitHub Packages permission to push `ghcr.io/obeak/gitbeak-mcp`
