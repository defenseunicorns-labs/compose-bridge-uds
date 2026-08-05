import React, { useEffect, useState } from "react";

function displayName(user) {
  return (
    user?.name ||
    user?.preferred_username ||
    user?.email ||
    user?.sub ||
    "authenticated user"
  );
}

function postedAt(value) {
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

export default function App() {
  const [profile, setProfile] = useState({ status: "loading", user: null });
  const [messages, setMessages] = useState({
    status: "loading",
    items: [],
  });
  const [draft, setDraft] = useState("");
  const [submission, setSubmission] = useState({
    status: "idle",
    error: null,
  });

  useEffect(() => {
    const controller = new AbortController();

    async function loadProfile() {
      try {
        const response = await fetch("/api/userinfo", {
          credentials: "same-origin",
          headers: { Accept: "application/json" },
          signal: controller.signal,
        });

        if (!response.ok) {
          throw new Error(`userinfo request failed with status ${response.status}`);
        }

        const user = await response.json();
        setProfile({ status: "ready", user });
      } catch (error) {
        if (error.name !== "AbortError") {
          setProfile({ status: "error", user: null });
        }
      }
    }

    async function loadMessages() {
      try {
        const response = await fetch("/api/messages", {
          credentials: "same-origin",
          headers: { Accept: "application/json" },
          signal: controller.signal,
        });

        if (!response.ok) {
          throw new Error(`messages request failed with status ${response.status}`);
        }

        const items = await response.json();
        setMessages({ status: "ready", items });
      } catch (error) {
        if (error.name !== "AbortError") {
          setMessages({ status: "error", items: [] });
        }
      }
    }

    loadProfile();
    loadMessages();
    return () => controller.abort();
  }, []);

  async function submitMessage(event) {
    event.preventDefault();

    const text = draft.trim();
    if (!text || submission.status === "submitting") {
      return;
    }

    setSubmission({ status: "submitting", error: null });

    try {
      const response = await fetch("/api/messages", {
        method: "POST",
        credentials: "same-origin",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ text }),
      });

      if (!response.ok) {
        throw new Error(`message request failed with status ${response.status}`);
      }

      const created = await response.json();
      setMessages((current) => ({
        status: "ready",
        items: [created, ...current.items],
      }));
      setDraft("");
      setSubmission({ status: "idle", error: null });
    } catch {
      setSubmission({
        status: "error",
        error: "Your message could not be saved. Please try again.",
      });
    }
  }

  const name = displayName(profile.user);
  const heading =
    profile.status === "ready"
      ? `Hello, ${name}.`
      : "Hello from a UDS-ready React app.";
  const statusText = {
    loading: "Loading your UDS profile",
    ready: `Authenticated as ${name}`,
    error: "Signed in, but your profile is unavailable",
  }[profile.status];
  const remaining = 500 - draft.length;

  return (
    <main className="shell">
      <section className="hero" aria-labelledby="page-title">
        <p className="eyebrow">React · FastAPI · Postgres · UDS</p>
        <h1 id="page-title">{heading}</h1>
        <p className="lede">
          Authservice supplies your identity, FastAPI applies it to every
          message, and Postgres keeps the conversation around.
        </p>
        <div
          className={`status status--${profile.status}`}
          role="status"
          aria-live="polite"
        >
          <span className="status-dot" aria-hidden="true" />
          {statusText}
        </div>
      </section>

      <section className="message-board" aria-labelledby="messages-title">
        <header className="message-board-header">
          <div>
            <p className="eyebrow">Persistent messages</p>
            <h2 id="messages-title">Leave a note</h2>
          </div>
          {profile.status === "ready" && <p>Posting as {name}</p>}
        </header>

        <form className="message-form" onSubmit={submitMessage}>
          <label htmlFor="message">Message</label>
          <textarea
            id="message"
            name="message"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            maxLength={500}
            rows={4}
            placeholder="What would you like to share?"
            required
          />
          <div className="message-form-actions">
            <span>{remaining} characters remaining</span>
            <button
              type="submit"
              disabled={!draft.trim() || submission.status === "submitting"}
            >
              {submission.status === "submitting" ? "Posting…" : "Post message"}
            </button>
          </div>
          {submission.error && (
            <p className="form-error" role="alert">
              {submission.error}
            </p>
          )}
        </form>

        <div className="messages" aria-live="polite">
          {messages.status === "loading" && <p>Loading messages…</p>}
          {messages.status === "error" && (
            <p className="form-error">Messages are temporarily unavailable.</p>
          )}
          {messages.status === "ready" && messages.items.length === 0 && (
            <p>No messages yet. Be the first to post one.</p>
          )}
          {messages.status === "ready" && messages.items.length > 0 && (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th scope="col">Message</th>
                    <th scope="col">Sender</th>
                    <th scope="col">Posted</th>
                  </tr>
                </thead>
                <tbody>
                  {messages.items.map((message) => (
                    <tr key={message.id}>
                      <td>{message.text}</td>
                      <td>{message.sender.name}</td>
                      <td>
                        <time dateTime={message.created_at}>
                          {postedAt(message.created_at)}
                        </time>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </section>

      <footer>
        <span>React → NGINX → FastAPI → Postgres</span>
        <span>Identity supplied by UDS Authservice</span>
      </footer>
    </main>
  );
}
