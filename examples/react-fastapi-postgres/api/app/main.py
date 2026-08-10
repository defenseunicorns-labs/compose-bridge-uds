from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from datetime import datetime
from typing import Annotated, Any

import jwt
from fastapi import Depends, FastAPI, HTTPException, Request, Response, status
from fastapi.responses import JSONResponse
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from pydantic import BaseModel, ConfigDict, StringConstraints

from app import database


@asynccontextmanager
async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
    await database.initialize_schema()
    yield


app = FastAPI(
    title="React FastAPI Postgres API",
    docs_url=None,
    lifespan=lifespan,
    openapi_url=None,
    redoc_url=None,
)

bearer = HTTPBearer(auto_error=False)


class UserInfo(BaseModel):
    sub: str
    name: str | None = None
    preferred_username: str | None = None
    email: str | None = None


class MessageCreate(BaseModel):
    model_config = ConfigDict(extra="forbid")

    text: Annotated[
        str,
        StringConstraints(strip_whitespace=True, min_length=1, max_length=500),
    ]


class MessageSender(BaseModel):
    sub: str
    name: str


class Message(BaseModel):
    id: int
    text: str
    sender: MessageSender
    created_at: datetime


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


def user_from_claims(claims: dict[str, Any]) -> UserInfo:
    return UserInfo(
        sub=claims["sub"].strip(),
        name=optional_string(claims, "name"),
        preferred_username=optional_string(claims, "preferred_username"),
        email=optional_string(claims, "email"),
    )


def display_name(user: UserInfo) -> str:
    return (
        user.name
        or user.preferred_username
        or user.email
        or user.sub
    )


def message_from_record(record: dict[str, Any]) -> Message:
    return Message(
        id=record["id"],
        text=record["text"],
        sender=MessageSender(
            sub=record["sender_sub"],
            name=record["sender_name"],
        ),
        created_at=record["created_at"],
    )


@app.exception_handler(database.DatabaseUnavailableError)
async def database_unavailable(
    _request: Request,
    _error: database.DatabaseUnavailableError,
) -> JSONResponse:
    return JSONResponse(
        status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
        content={"detail": "Database unavailable"},
    )


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


@app.get("/api/userinfo", response_model=UserInfo)
async def userinfo(
    response: Response,
    claims: Annotated[dict[str, Any], Depends(decode_claims)],
) -> UserInfo:
    response.headers["Cache-Control"] = "no-store"
    return user_from_claims(claims)


@app.get("/api/messages", response_model=list[Message])
async def messages(
    response: Response,
    _claims: Annotated[dict[str, Any], Depends(decode_claims)],
) -> list[Message]:
    response.headers["Cache-Control"] = "no-store"
    records = await database.list_messages()
    return [message_from_record(record) for record in records]


@app.post(
    "/api/messages",
    response_model=Message,
    status_code=status.HTTP_201_CREATED,
)
async def post_message(
    message: MessageCreate,
    claims: Annotated[dict[str, Any], Depends(decode_claims)],
) -> Message:
    user = user_from_claims(claims)
    record = await database.create_message(
        text=message.text,
        sender_sub=user.sub,
        sender_name=display_name(user),
    )
    return message_from_record(record)
