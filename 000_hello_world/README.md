## common

`python -m venv .venv` # pycharm uses '.venv'

`source .venv/bin/activate`

`pip install -r requirements.txt`

`# go to https://platform.openai.com & save key into ~/.openai`

`# go to platform.claude.com & save key into ~/.anthropic`

`# go to https://openrouter.ai & save key into ~/.openrouter`

Lint + types (config in `pyproject.toml`):

`ruff check .` `ruff format .` `mypy .`

## openai_py

Into package 'openai' is hardcoded OpenAI-endpoints. You can override base_url like that:

```python
client = OpenAI(
    api_key="fake-key",
    base_url="https://openrouter.ai/api/v1"
)
```

It implements 'OpenAI API'.

But anyway, it's not a universal client, bcs it not implement:
- Anthropic Messages API
- Google Gemini API
- Mistral API
- HuggingFace Inference API

```bash
curl https://api.openai.com/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer %TOKEN%" \
  -d '{
    "model": "gpt-5.4-mini",
    "input": "write a haiku about ai",
    "store": true
  }'
```

`gpt-4o-mini`/`gpt-5.5` somewhy exceeds quota, but `gpt-5.4-mini` not.

## anthropic_py

The same, but free token don't have free requests.

Anthropic API also has own api & own client library - like OpenAPI.

## litellm

litellm - it's wrapper over OpenAI/Anthropic packages.

## openrouter_via_langchain_py

`pip install langchain langchain-openai`

LangChain doesn't know about OpenRouter directly. So we take the OpenAI-compatible
`ChatOpenAI` client and override `base_url` with the OpenRouter endpoint:

```python
llm = ChatOpenAI(
    model="google/gemma-4-31b-it:free",
    api_key=api_key,
    base_url="https://openrouter.ai/api/v1",
)
response = llm.invoke("hello")
print(response.content)
```

Unlike litellm, the model name has no `openrouter/` prefix — the provider is
chosen by `base_url`, not by the model string.

## openrouter_via_langchaingo_go

`go run .` (deps fetched automatically via `go.mod`)

Same idea as `openrouter_via_langchain_py`, in Go. langchaingo has no OpenRouter
provider, so we use its OpenAI-compatible client and override the base URL:

```go
llm, _ := openai.New(
    openai.WithToken(apiKey),
    openai.WithBaseURL("https://openrouter.ai/api/v1"),
    openai.WithModel("google/gemma-4-31b-it:free"),
)
answer, _ := llms.GenerateFromSinglePrompt(ctx, llm, "hello")
fmt.Println(answer)
```

As in the LangChain version, the model name has no `openrouter/` prefix — the
provider is chosen by the base URL.
