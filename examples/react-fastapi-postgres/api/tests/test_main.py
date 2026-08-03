import httpx
import jwt
import pytest

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
