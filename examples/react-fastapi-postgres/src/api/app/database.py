import logging
import os
from pathlib import Path
from typing import Any

import psycopg
from psycopg.rows import dict_row

logger = logging.getLogger(__name__)

MESSAGES_SCHEMA = """
CREATE TABLE IF NOT EXISTS messages (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    text TEXT NOT NULL,
    sender_sub TEXT NOT NULL,
    sender_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT messages_text_length
        CHECK (char_length(btrim(text)) BETWEEN 1 AND 500)
)
"""


class DatabaseUnavailableError(Exception):
    pass


def connection_parameters() -> dict[str, Any]:
    username_file = Path(
        os.getenv(
            "POSTGRES_USER_FILE",
            "/run/secrets/postgres-username",
        )
    )
    password_file = Path(
        os.getenv(
            "POSTGRES_PASSWORD_FILE",
            "/run/secrets/postgres-password",
        )
    )
    return {
        "host": os.getenv("POSTGRES_HOST", "db"),
        "port": int(os.getenv("POSTGRES_PORT", "5432")),
        "dbname": os.getenv("POSTGRES_DB", "messages"),
        "user": username_file.read_text().strip(),
        "password": password_file.read_text().strip(),
        "connect_timeout": 5,
        "row_factory": dict_row,
    }


async def initialize_schema() -> None:
    try:
        async with await psycopg.AsyncConnection.connect(
            **connection_parameters()
        ) as connection:
            async with connection.cursor() as cursor:
                await cursor.execute(MESSAGES_SCHEMA)
    except (OSError, ValueError, psycopg.Error) as error:
        logger.exception("Unable to initialize database schema")
        raise DatabaseUnavailableError from error


async def list_messages() -> list[dict[str, Any]]:
    try:
        async with await psycopg.AsyncConnection.connect(
            **connection_parameters()
        ) as connection:
            async with connection.cursor() as cursor:
                await cursor.execute(
                    """
                    SELECT id, text, sender_sub, sender_name, created_at
                    FROM messages
                    ORDER BY created_at DESC, id DESC
                    """
                )
                return list(await cursor.fetchall())
    except (OSError, ValueError, psycopg.Error) as error:
        logger.exception("Unable to list messages")
        raise DatabaseUnavailableError from error


async def create_message(
    text: str,
    sender_sub: str,
    sender_name: str,
) -> dict[str, Any]:
    try:
        async with await psycopg.AsyncConnection.connect(
            **connection_parameters()
        ) as connection:
            async with connection.cursor() as cursor:
                await cursor.execute(
                    """
                    INSERT INTO messages (text, sender_sub, sender_name)
                    VALUES (%s, %s, %s)
                    RETURNING id, text, sender_sub, sender_name, created_at
                    """,
                    (text, sender_sub, sender_name),
                )
                record = await cursor.fetchone()
                if record is None:
                    raise DatabaseUnavailableError
                return record
    except DatabaseUnavailableError:
        raise
    except (OSError, ValueError, psycopg.Error) as error:
        logger.exception("Unable to create message")
        raise DatabaseUnavailableError from error
