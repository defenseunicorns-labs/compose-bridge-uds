FROM python:3.13-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

COPY requirements.txt ./
RUN pip install --no-cache-dir --requirement requirements.txt

FROM base AS test

COPY requirements-dev.txt ./
RUN pip install --no-cache-dir --requirement requirements-dev.txt

COPY pytest.ini ./
COPY app ./app
COPY tests ./tests

RUN pytest --quiet

FROM base AS runtime

RUN groupadd --system --gid 10001 app \
    && useradd --system --uid 10001 --gid app --home-dir /app app

COPY --from=test /app/app ./app

USER 10001:10001

EXPOSE 8000

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
