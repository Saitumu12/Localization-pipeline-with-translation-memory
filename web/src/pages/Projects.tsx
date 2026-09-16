import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { api } from "../api";
import type { Project } from "../types";

export function Projects() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [name, setName] = useState("");
  const [source, setSource] = useState("en");
  const [target, setTarget] = useState("de");
  const [error, setError] = useState("");

  const reload = () => api.projects().then(setProjects).catch((e) => setError(e.message));
  useEffect(() => {
    reload();
  }, []);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    try {
      await api.createProject({
        name,
        source_locale: source,
        target_locale: target,
      });
      setName("");
      reload();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  return (
    <div className="page">
      <h1>Projects</h1>
      {error && <p className="error">{error}</p>}

      <ul className="cards">
        {projects.map((p) => (
          <li key={p.id}>
            <Link to={`/projects/${p.id}`}>
              <strong>{p.name}</strong>
              <span className="locales">
                {p.source_locale} → {p.target_locale}
              </span>
            </Link>
          </li>
        ))}
        {projects.length === 0 && <li className="empty">No projects yet.</li>}
      </ul>

      <form onSubmit={create} className="inline-form">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="project name"
          required
        />
        <input
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="source locale"
          required
        />
        <input
          value={target}
          onChange={(e) => setTarget(e.target.value)}
          placeholder="target locale"
          required
        />
        <button type="submit">Create project</button>
      </form>
    </div>
  );
}
