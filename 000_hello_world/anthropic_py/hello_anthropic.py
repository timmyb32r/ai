#!/usr/bin/env python3
from pathlib import Path

import anthropic
import truststore
from anthropic.types import MessageParam


def read_api_key(path: Path = Path.home() / ".anthropic") -> str:
    key: str = path.read_text(encoding="utf-8").strip()
    if not key:
        raise ValueError(f"Файл {path} пуст — не найден API-ключ")
    return key


def ask(client: anthropic.Anthropic, prompt: str, model: str = "claude-opus-4-8") -> str:
    message = client.messages.create(
        model=model,
        max_tokens=1024,
        messages=[MessageParam(role="user", content=prompt)],
    )
    return "".join(block.text for block in message.content if block.type == "text")


def main() -> None:
    truststore.inject_into_ssl()  # inject system certificates into process
    client: anthropic.Anthropic = anthropic.Anthropic(api_key=read_api_key())
    answer: str = ask(client, "hello")
    print(answer)


if __name__ == "__main__":
    main()
