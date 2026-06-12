#!/usr/bin/env python3
from __future__ import annotations

from pathlib import Path

from openai import OpenAI
from openai.types.chat import ChatCompletionUserMessageParam


def read_api_key(path: Path = Path.home() / ".openai") -> str:
    key: str = path.read_text(encoding="utf-8").strip()
    if not key:
        raise ValueError(f"Файл {path} пуст — не найден API-ключ")
    return key


# gpt-4o-mini
# gpt-5.4-mini
def ask(client: OpenAI, prompt: str, model: str = "gpt-5.4-mini") -> str:
    completion = client.chat.completions.create(
        model=model,
        messages=[ChatCompletionUserMessageParam(role="user", content=prompt)],
    )
    content: str | None = completion.choices[0].message.content
    return content or ""


def main() -> None:
    client: OpenAI = OpenAI(api_key=read_api_key())
    answer: str = ask(client, "hello")
    print(answer)  # "Hello! How can I help you today?"


if __name__ == "__main__":
    main()
