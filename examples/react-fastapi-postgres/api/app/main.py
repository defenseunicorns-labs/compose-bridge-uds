from typing import Annotated, Any

import jwt
from fastapi import Depends, FastAPI, HTTPException, Response, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from pydantic import BaseModel

app = FastAPI(
    title="React FastAPI Postgres API",
    docs_url=None,
    openapi_url=None,
    redoc_url=None,
)

bearer = HTTPBearer(auto_error=False)


class UserInfo(BaseModel):
    sub: str
    name: str | None = None
    preferred_username: str | None = None
    email: str | None = None


def optional_string(claims: dict[str, Any], name: str) -> str | None:
    value = claims.get(name)
    if not isinstance(value, str):
        return None
    value = value.strip()
    return value or None


async def decode_claims(
    credentials: Annotated[
        HTTPAuthorizationCredentials | None,
        Depends(bearer),
    ],
) -> dict[str, Any]:
    if credentials is None:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Bearer token required",
        )

    try:
        claims = jwt.decode(
            credentials.credentials,
            options={
                "verify_signature": False,
                "verify_aud": False,
                "verify_exp": False,
            },
        )
    except jwt.InvalidTokenError as error:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Invalid bearer token",
        ) from error

    subject = claims.get("sub")
    if not isinstance(subject, str) or not subject.strip():
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Bearer token is missing a subject",
        )

    return claims


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/api/userinfo", response_model=UserInfo)
async def userinfo(
    response: Response,
    claims: Annotated[dict[str, Any], Depends(decode_claims)],
) -> UserInfo:
    response.headers["Cache-Control"] = "no-store"
    return UserInfo(
        sub=claims["sub"].strip(),
        name=optional_string(claims, "name"),
        preferred_username=optional_string(claims, "preferred_username"),
        email=optional_string(claims, "email"),
    )
