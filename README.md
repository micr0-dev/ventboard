# Vent Board

Minimal anonymous board with a Go backend, a single server-rendered page, SQLite persistence, and local Ollama-based content warning tagging.

## What it does

- Anonymous text posts only
- Single reverse-chronological feed
- No accounts, comments, likes, or JavaScript
- Hashed-IP cooldown and rolling rate limit
- Ollama classifier adds CW labels before a post is published
- Sensitive posts render collapsed behind native HTML `<details>`

## Run it

1. Start Ollama.
2. Pull the model you want to use, for example:

```bash
ollama pull gemma3:12b
```

3. Set environment variables as needed:

```bash
export APP_ENV=dev
export IP_HASH_SALT='change-me'
export FORM_TOKEN_SECRET='change-me-too'
export OLLAMA_MODEL='gemma3:12b'
export PUBLIC_SOURCE_URL='https://github.com/yourname/theVentBoard'
```

4. Start the server:

```bash
go run ./cmd/server
```

5. Open `http://127.0.0.1:8080`.

## Main environment variables

- `PORT` default `8080`
- `DATABASE_PATH` default `./data/ventboard.db`
- `OLLAMA_URL` default `http://127.0.0.1:11434`
- `OLLAMA_MODEL` default `gemma3:12b`
- `POST_MAX_CHARS` default `2000`
- `POST_COOLDOWN_SECONDS` default `60`
- `POST_RATE_LIMIT_COUNT` default `5`
- `POST_RATE_LIMIT_WINDOW_MINUTES` default `15`
- `RATE_LIMIT_HASH_ROTATION_MINUTES` default `60`
- `CLASSIFIER_TIMEOUT_SECONDS` default `20`
- `CLASSIFIER_POLL_INTERVAL_SECONDS` default `3`
- `CLASSIFIER_MAX_RETRIES` default `5`
- `FEED_LIMIT` default `100`
- `IP_HASH_SALT` required outside `APP_ENV=dev`
- `FORM_TOKEN_SECRET` defaults to `IP_HASH_SALT`
- `FORM_TOKEN_MIN_AGE_SECONDS` default `3`
- `FORM_TOKEN_MAX_AGE_MINUTES` default `120`
- `PUBLIC_SOURCE_URL` optional public repo link for the homepage source-code link

## License

GNU AGPLv3-or-later. See `LICENSE`.

## Test

```bash
go test ./...
```
