#!/bin/sh
# Validate existing weights and create .env. Does not copy or delete any models.
set -eu
if [ "$#" -ne 2 ]; then
  echo "Usage: $0 /absolute/path/to/server/.docker-cache /absolute/path/to/hanlp-cache" >&2
  exit 2
fi
python3 - "$1" "$2" <<'PY'
import os, pathlib, sys
assets = pathlib.Path(sys.argv[1]).expanduser().resolve(strict=True)
hanlp = pathlib.Path(sys.argv[2]).expanduser().resolve()
required = ['sense-voice-2024/model.int8.onnx', 'sense-voice-2024/tokens.txt',
            'dabkrs.gz', 'cedict_ts.u8', 'zh-extract.jsonl.gz', 'Unihan_Readings.txt']
for name in required:
    p = assets / name
    if not p.is_file() or p.stat().st_size == 0:
        raise SystemExit(f'Missing model/dictionary: {p}')
hanlp.mkdir(parents=True, exist_ok=True)
env = pathlib.Path('.env')
if env.exists():
    raise SystemExit('.env already exists; edit its paths or move it aside explicitly')
def quote(value):
    if any(c in value for c in "\n\r'\\"):
        raise SystemExit('Unsupported characters in path')
    return "'" + value + "'"
with env.open('x') as f:
    f.write('MODEL_ASSETS_DIR=' + quote(str(assets)) + '\n')
    f.write('ASR_MODEL_DIR=' + quote(str(assets / 'sense-voice-2024')) + '\n')
    f.write('HANLP_ASSETS_DIR=' + quote(str(hanlp)) + '\nHTTP_PORT=8081\n')
print('Validated existing SenseVoice/dictionaries; wrote .env without copying assets.')
print('Prepare HanLP once: docker compose --profile prepare run --rm --build prepare-hanlp')
print('Then: docker compose up -d --build')
PY
