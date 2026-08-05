from datetime import datetime, timezone
from typing import Any

import httpx
import jwt
import pytest

from app import database
from app.main import app


@pytest.fixture
def anyio_backend() -> str:
    return "asyncio"


def authorization(claims: dict[str, object]) -> dict[str, str]:
    token = jwt.encode(claims, "test-only-secret", algorithm="HS256")
    return {"Authorization": f"Bearer {token}"}


async def get(
    path: str,
    headers: dict[str, str] | None = None,
) -> httpx.Response:
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(
        transport=transport,
        base_url="http://testserver",
    ) as client:
        return await client.get(path, headers=headers)


async def post(
    path: str,
    json: dict[str, Any],
    headers: dict[str, str] | None = None,
) -> httpx.Response:
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(
        transport=transport,
        base_url="http://testserver",
    ) as client:
        return await client.post(path, json=json, headers=headers)


@pytest.mark.anyio
async def test_health() -> None:
    response = await get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


@pytest.mark.anyio
async def test_userinfo_returns_only_selected_claims() -> None:
    response = await get(
        "/api/userinfo",
        headers=authorization(
            {
                "sub": "user-123",
                "name": "Doug Unicorn",
                "preferred_username": "doug",
                "email": "doug@example.com",
                "groups": ["/UDS Core/Admin"],
                "access_token": "do-not-return",
            }
        ),
    )

    assert response.status_code == 200
    assert response.json() == {
        "sub": "user-123",
        "name": "Doug Unicorn",
        "preferred_username": "doug",
        "email": "doug@example.com",
    }
    assert response.headers["cache-control"] == "no-store"


@pytest.mark.anyio
async def test_userinfo_allows_missing_optional_claims() -> None:
    response = await get(
        "/api/userinfo",
        headers=authorization({"sub": "user-123"}),
    )

    assert response.status_code == 200
    assert response.json() == {
        "sub": "user-123",
        "name": None,
        "preferred_username": None,
        "email": None,
    }


@pytest.mark.anyio
async def test_userinfo_ignores_non_string_optional_claims() -> None:
    response = await get(
        "/api/userinfo",
        headers=authorization(
            {
                "sub": "user-123",
                "name": ["not", "a", "string"],
                "preferred_username": 123,
                "email": False,
            }
        ),
    )

    assert response.status_code == 200
    assert response.json() == {
        "sub": "user-123",
        "name": None,
        "preferred_username": None,
        "email": None,
    }


@pytest.mark.anyio
async def test_userinfo_requires_bearer_token() -> None:
    response = await get("/api/userinfo")

    assert response.status_code == 401
    assert response.json() == {"detail": "Bearer token required"}


@pytest.mark.anyio
async def test_userinfo_rejects_malformed_token() -> None:
    response = await get(
        "/api/userinfo",
        headers={"Authorization": "Bearer not-a-jwt"},
    )

    assert response.status_code == 401
    assert response.json() == {"detail": "Invalid bearer token"}


@pytest.mark.anyio
async def test_userinfo_requires_string_subject() -> None:
    for subject in (None, "", "   ", 123):
        response = await get(
            "/api/userinfo",
            headers=authorization({"sub": subject}),
        )

        assert response.status_code == 401
        assert response.json() == {
            "detail": "Bearer token is missing a subject"
        }


@pytest.mark.anyio
async def test_messages_returns_newest_messages(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    async def fake_list_messages() -> list[dict[str, Any]]:
        return [
            {
                "id": 2,
                "text": "Second message",
                "sender_sub": "user-2",
                "sender_name": "Two Unicorn",
                "created_at": datetime(2026, 8, 5, 16, 30, tzinfo=timezone.utc),
            },
            {
                "id": 1,
                "text": "First message",
                "sender_sub": "user-1",
                "sender_name": "One Unicorn",
                "created_at": datetime(2026, 8, 5, 16, 0, tzinfo=timezone.utc),
            },
        ]

    monkeypatch.setattr(database, "list_messages", fake_list_messages)

    response = await get(
        "/api/messages",
        headers=authorization({"sub": "reader"}),
    )

    assert response.status_code == 200
    assert response.json() == [
        {
            "id": 2,
            "text": "Second message",
            "sender": {"sub": "user-2", "name": "Two Unicorn"},
            "created_at": "2026-08-05T16:30:00Z",
        },
        {
            "id": 1,
            "text": "First message",
            "sender": {"sub": "user-1", "name": "One Unicorn"},
            "created_at": "2026-08-05T16:00:00Z",
        },
    ]
    assert response.headers["cache-control"] == "no-store"


@pytest.mark.anyio
async def test_post_message_uses_authenticated_sender(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    captured: dict[str, str] = {}

    async def fake_create_message(
        text: str,
        sender_sub: str,
        sender_name: str,
    ) -> dict[str, Any]:
        captured.update(
            text=text,
            sender_sub=sender_sub,
            sender_name=sender_name,
        )
        return {
            "id": 1,
            "text": text,
            "sender_sub": sender_sub,
            "sender_name": sender_name,
            "created_at": datetime(2026, 8, 5, 16, 0, tzinfo=timezone.utc),
        }

    monkeypatch.setattr(database, "create_message", fake_create_message)

    response = await post(
        "/api/messages",
        json={"text": "  Hello from UDS  "},
        headers=authorization(
            {
                "sub": "user-123",
                "name": "Doug Unicorn",
                "preferred_username": "doug",
            }
        ),
    )

    assert response.status_code == 201
    assert captured == {
        "text": "Hello from UDS",
        "sender_sub": "user-123",
        "sender_name": "Doug Unicorn",
    }
    assert response.json() == {
        "id": 1,
        "text": "Hello from UDS",
        "sender": {"sub": "user-123", "name": "Doug Unicorn"},
        "created_at": "2026-08-05T16:00:00Z",
    }


@pytest.mark.anyio
@pytest.mark.parametrize(
    ("claims", "expected"),
    [
        ({"sub": "user-123", "preferred_username": "doug"}, "doug"),
        ({"sub": "user-123", "email": "doug@example.com"}, "doug@example.com"),
        ({"sub": "user-123"}, "user-123"),
    ],
)
async def test_post_message_sender_name_fallbacks(
    monkeypatch: pytest.MonkeyPatch,
    claims: dict[str, str],
    expected: str,
) -> None:
    async def fake_create_message(
        text: str,
        sender_sub: str,
        sender_name: str,
    ) -> dict[str, Any]:
        return {
            "id": 1,
            "text": text,
            "sender_sub": sender_sub,
            "sender_name": sender_name,
            "created_at": datetime(2026, 8, 5, 16, 0, tzinfo=timezone.utc),
        }

    monkeypatch.setattr(database, "create_message", fake_create_message)

    response = await post(
        "/api/messages",
        json={"text": "Hello"},
        headers=authorization(claims),
    )

    assert response.status_code == 201
    assert response.json()["sender"]["name"] == expected


@pytest.mark.anyio
async def test_message_endpoints_require_bearer_token() -> None:
    list_response = await get("/api/messages")
    create_response = await post("/api/messages", json={"text": "Hello"})

    assert list_response.status_code == 401
    assert create_response.status_code == 401


@pytest.mark.anyio
@pytest.mark.parametrize("text", ["", "   ", "x" * 501])
async def test_post_message_validates_text(text: str) -> None:
    response = await post(
        "/api/messages",
        json={"text": text},
        headers=authorization({"sub": "user-123"}),
    )

    assert response.status_code == 422


@pytest.mark.anyio
async def test_post_message_rejects_client_sender() -> None:
    response = await post(
        "/api/messages",
        json={
            "text": "Hello",
            "sender": {"sub": "spoofed", "name": "Spoofed User"},
        },
        headers=authorization({"sub": "user-123"}),
    )

    assert response.status_code == 422


@pytest.mark.anyio
async def test_messages_reports_database_unavailable(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    async def unavailable() -> list[dict[str, Any]]:
        raise database.DatabaseUnavailableError

    monkeypatch.setattr(database, "list_messages", unavailable)

    response = await get(
        "/api/messages",
        headers=authorization({"sub": "user-123"}),
    )

    assert response.status_code == 503
    assert response.json() == {"detail": "Database unavailable"}
