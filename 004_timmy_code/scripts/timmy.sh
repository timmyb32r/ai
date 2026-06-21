#!/bin/sh
set -e

# Check required environment variable
if [ -z "$DEEPSEEK_API_KEY" ]; then
    echo "Error: DEEPSEEK_API_KEY is not set." >&2
    echo "Please set it: export DEEPSEEK_API_KEY='your-api-key'" >&2
    exit 1
fi

# Check if Docker image exists (must be pre-built via 'make install')
if ! docker image inspect timmy-code >/dev/null 2>&1; then
    echo "Error: Docker image 'timmy-code' not found." >&2
    echo "Build and install it first:" >&2
    echo "  cd /path/to/timmy-code && make install" >&2
    exit 1
fi

# Interactive Docker REPL. Arguments become first REPL input (piped via stdin).
if [ $# -gt 0 ]; then
    printf '%s\n' "$*" | exec docker run --rm -i -v "$PWD:/work" -e DEEPSEEK_API_KEY timmy-code
elif [ -t 0 ]; then
    exec docker run --rm -it -v "$PWD:/work" -e DEEPSEEK_API_KEY timmy-code
else
    exec docker run --rm -i -v "$PWD:/work" -e DEEPSEEK_API_KEY timmy-code
fi
