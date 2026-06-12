#!/usr/bin/env python3
from pathlib import Path

import litellm

litellm.suppress_debug_info = True  # turn-off banners "Provider List: ..." in stdout


def read_api_key(path: Path = Path.home() / ".openrouter") -> str:
    key: str = path.read_text(encoding="utf-8").strip()
    if not key:
        raise ValueError(f"Файл {path} пуст — не найден API-ключ")
    return key


def ask(prompt: str, api_key: str, model: str = "openrouter/google/gemma-4-31b-it:free") -> str:
    response = litellm.completion(
        model=model,
        api_key=api_key,
        messages=[{"role": "user", "content": prompt}],
    )
    content: str | None = response.choices[0].message.content
    return content or ""


def main() -> None:
    answer: str = ask("hello", api_key=read_api_key())
    print(answer)  # "Hello! How can I help you today?"


if __name__ == "__main__":
    main()
