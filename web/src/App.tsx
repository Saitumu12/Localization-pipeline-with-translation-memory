import { useEffect, useState } from "react";
import { Link, Route, Routes } from "react-router-dom";

import { api } from "./api";
import type { Health } from "./types";
import { FileReview } from "./pages/FileReview";
import { ProjectPage } from "./pages/Project";
import { Projects } from "./pages/Projects";

// There is no authentication in this project. Approving a translation records a
// name, so the name is asked for here and kept in the browser.
const REVIEWER_KEY = "reviewer";

export function useReviewer(): [string, (v: string) => void] {
  const [reviewer, setReviewer] = useState(
    () => localStorage.getItem(REVIEWER_KEY) ?? "",
  );
  useEffect(() => {
    localStorage.setItem(REVIEWER_KEY, reviewer);
  }, [reviewer]);
  return [reviewer, setReviewer];
}

export function App() {
  const [reviewer, setReviewer] = useReviewer();
  const [health, setHealth] = useState<Health | null>(null);

  useEffect(() => {
    api.health().then(setHealth).catch(() => setHealth(null));
  }, []);

  return (
    <div className="app">
      <header>
        <Link to="/" className="brand">
          Localization Pipeline
        </Link>
        <div className="spacer" />
        {health && (
          <span className="services">
            <span title={health.embedding_model}>
              memory: {health.embeddings ? health.embedding_model : "off"}
            </span>
            <span title={health.llm_model}>
              machine translation:{" "}
              {health.machine_translation ? health.llm_model : "off"}
            </span>
          </span>
        )}
        <label className="reviewer">
          Reviewer
          <input
            value={reviewer}
            placeholder="your name"
            onChange={(e) => setReviewer(e.target.value)}
          />
        </label>
      </header>

      <main>
        <Routes>
          <Route path="/" element={<Projects />} />
          <Route path="/projects/:projectId" element={<ProjectPage />} />
          <Route path="/files/:fileId" element={<FileReview />} />
        </Routes>
      </main>
    </div>
  );
}
