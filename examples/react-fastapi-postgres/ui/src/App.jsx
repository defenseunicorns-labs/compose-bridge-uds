import React, { useEffect, useState } from "react";

const milestones = [
  {
    label: "Source",
    value: "Compose",
    detail: "The application starts with a familiar developer workflow.",
  },
  {
    label: "Identity",
    value: "FastAPI",
    detail: "The API turns the trusted UDS token into a small user profile.",
  },
  {
    label: "Runtime",
    value: "UDS",
    detail: "The packaged application is deployed through the tenant gateway.",
  },
];

function displayName(user) {
  return (
    user?.name ||
    user?.preferred_username ||
    user?.email ||
    user?.sub ||
    "authenticated user"
  );
}

export default function App() {
  const [profile, setProfile] = useState({ status: "loading", user: null });

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

    loadProfile();
    return () => controller.abort();
  }, []);

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

  return (
    <main className="shell">
      <section className="hero" aria-labelledby="page-title">
        <p className="eyebrow">React · FastAPI · Compose Bridge · UDS</p>
        <h1 id="page-title">{heading}</h1>
        <p className="lede">
          Authservice protects the public route, NGINX proxies same-origin API
          requests, and FastAPI reads the trusted user token supplied by UDS.
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

      <section className="milestones" aria-label="Build milestones">
        {milestones.map((milestone) => (
          <article className="milestone" key={milestone.label}>
            <p className="milestone-label">{milestone.label}</p>
            <h2>{milestone.value}</h2>
            <p>{milestone.detail}</p>
          </article>
        ))}
      </section>

      <footer>
        <span>Current pass: API-backed identity</span>
        <span>Next: Postgres persistence</span>
      </footer>
    </main>
  );
}
