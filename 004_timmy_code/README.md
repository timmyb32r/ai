# timmy-code

Deep-interview agent powered by DeepSeek LLM. Runs in Docker for full isolation.

## Quick Start

```bash
# 1. Build the binary and Docker image
go build ./cmd/timmy-code/
docker build -t timmy-code .

# 2. Run (auto-launches inside Docker container)
./timmy-code
```

The binary detects when it's on the host and automatically re-launches itself inside a Docker container with full tool permissions. Inside the container it shows:

```
timmy-code v0.3 — DeepSeek CLI assistant (Docker container)
```

## Optional: install as global command

```bash
sudo ln -s "$(pwd)/timmy-code" /usr/local/bin/timmy
# Now run from anywhere:
timmy
```

Make sure the Docker image is built first.

## Requirements

- [Colima](https://github.com/abiosoft/colima) (or Docker runtime)
- `DEEPSEEK_API_KEY` environment variable

## Non-interactive mode

```bash
./timmy-code --execute "какая сейчас дата?"
```

In non-interactive mode the binary runs directly on the host (no Docker wrap).

## Unrestricted Tools

Inside the container, **all tools run with full permissions** — Bash, Read, Write, Edit, and Agent. The Docker container acts as the security boundary, isolating tool execution from the host. Files written to `/work` inside the container persist on the host through the bind mount.
