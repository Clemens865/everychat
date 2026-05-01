# LiteLLM proxy — Phase 2 LLM gateway for Everychat.
# Routes Claude (chat) and OpenAI (embeddings) behind a single internal endpoint.
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1

RUN pip install --no-cache-dir "litellm[proxy]==1.74.9"

WORKDIR /app
COPY config.yaml /app/config.yaml

EXPOSE 4000

# Bind to 0.0.0.0 so the everychat container can reach us; --num_workers 1
# is sufficient for solo-founder Phase 2 traffic.
CMD ["litellm", "--config", "/app/config.yaml", "--host", "0.0.0.0", "--port", "4000", "--num_workers", "1"]
