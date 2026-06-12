#!/usr/bin/env python3
from pathlib import Path

from langchain_openai import ChatOpenAI
from pydantic import SecretStr


def read_api_key(path: Path = Path.home() / ".openrouter") -> str:
    key: str = path.read_text(encoding="utf-8").strip()
    if not key:
        raise ValueError(f"Файл {path} пуст — не найден API-ключ")
    return key


def ask(prompt: str, api_key: str, model: str = "google/gemma-4-31b-it:free") -> str:
    llm = ChatOpenAI(
        model=model,
        api_key=SecretStr(api_key),
        base_url="https://openrouter.ai/api/v1",
    )
    response = llm.invoke(prompt)
    content: str = response.text  # .content is str | list[...]; .text flattens to str
    return content or ""


def main() -> None:
    answer: str = ask("hello", api_key=read_api_key())
    print(answer)  # "Hello! How can I help you today?"


if __name__ == "__main__":
    main()
