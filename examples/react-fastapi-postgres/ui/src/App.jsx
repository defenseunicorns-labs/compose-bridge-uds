import React from "react";

const milestones = [
  {
    label: "Source",
    value: "Compose",
    detail: "The application starts with a familiar developer workflow.",
  },
  {
    label: "Build",
    value: "Bridge",
    detail: "Compose primitives become a generated Helm and UDS package.",
  },
  {
    label: "Runtime",
    value: "UDS",
    detail: "The packaged application is deployed through the tenant gateway.",
  },
];

export default function App() {
  return (
    <main className="shell">
      <section className="hero" aria-labelledby="page-title">
        <p className="eyebrow">React · Compose Bridge · UDS</p>
        <h1 id="page-title">Hello from a UDS-ready React app.</h1>
        <p className="lede">
          This is the first pass of a vendor-shaped application. The source is
          a Compose project, while the deployable result is a generated Zarf
          package.
        </p>
        <div className="status" role="status">
          <span className="status-dot" aria-hidden="true" />
          Running through the UDS tenant gateway
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
        <span>Pass 1: frontend workflow</span>
        <span>Next: API and database</span>
      </footer>
    </main>
  );
}
