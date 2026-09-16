import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { api } from "../api";
import { useReviewer } from "../App";
import type { GlossaryTerm, Inconsistency, LocFile, Project } from "../types";

export function ProjectPage() {
  const projectId = Number(useParams().projectId);
  const [reviewer] = useReviewer();

  const [project, setProject] = useState<Project | null>(null);
  const [files, setFiles] = useState<LocFile[]>([]);
  const [terms, setTerms] = useState<GlossaryTerm[]>([]);
  const [clashes, setClashes] = useState<Inconsistency[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const reload = useCallback(() => {
    api.project(projectId).then(setProject).catch((e) => setError(e.message));
    api.files(projectId).then(setFiles).catch((e) => setError(e.message));
    api.glossary(projectId).then(setTerms).catch(() => setTerms([]));
    api.inconsistencies(projectId).then(setClashes).catch(() => setClashes([]));
  }, [projectId]);

  useEffect(reload, [reload]);

  const upload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setBusy(`Importing ${file.name}`);
    setError("");
    try {
      await api.upload(projectId, file);
      reload();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy("");
      e.target.value = "";
    }
  };

  const adopt = async (fileId: number, name: string) => {
    if (!reviewer) {
      setError("Enter your name in the Reviewer field first.");
      return;
    }
    if (
      !confirm(
        `Sign off every translation already in ${name} as "${reviewer}"?\n` +
          "They will count as reviewed and go into the translation memory.",
      )
    ) {
      return;
    }
    setBusy(`Signing off ${name}`);
    setError("");
    try {
      const res = await api.adopt(fileId, reviewer);
      setBusy(
        `Approved ${res.approved}, blocked by QA issues ${res.blocked}, added to memory ${res.indexed}`,
      );
      reload();
    } catch (err) {
      setError((err as Error).message);
      setBusy("");
    }
  };

  const addTerm = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    try {
      await api.addTerm(projectId, {
        source_term: String(form.get("source") ?? ""),
        target_term: String(form.get("target") ?? ""),
      });
      e.currentTarget.reset();
      reload();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  if (!project) return <div className="page">Loading…</div>;

  return (
    <div className="page">
      <h1>{project.name}</h1>
      <p className="locales">
        {project.source_locale} → {project.target_locale}
      </p>
      {error && <p className="error">{error}</p>}
      {busy && <p className="busy">{busy}</p>}

      <section>
        <h2>Files</h2>
        <table className="files">
          <thead>
            <tr>
              <th>Name</th>
              <th>Format</th>
              <th>Progress</th>
              <th>Findings</th>
              <th>Export</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {files.map((f) => (
              <tr key={f.id}>
                <td>
                  <Link to={`/files/${f.id}`}>{f.name}</Link>
                </td>
                <td>{f.format}</td>
                <td>
                  <Progress stats={f.stats} />
                </td>
                <td>
                  {f.stats.errors > 0 && (
                    <span className="badge error">{f.stats.errors} errors</span>
                  )}
                  {f.stats.warnings > 0 && (
                    <span className="badge warning">
                      {f.stats.warnings} warnings
                    </span>
                  )}
                </td>
                <td className="exports">
                  <a href={api.exportURL(f.id, false)}>all</a>
                  <a href={api.exportURL(f.id, true)}>approved only</a>
                </td>
                <td>
                  <button onClick={() => adopt(f.id, f.name)}>Sign off imported</button>
                </td>
              </tr>
            ))}
            {files.length === 0 && (
              <tr>
                <td colSpan={6} className="empty">
                  No files imported yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
        <label className="upload">
          Import an XLIFF (.xlf/.xliff) or Qt (.ts) file
          <input type="file" accept=".xlf,.xliff,.ts" onChange={upload} />
        </label>
      </section>

      <section>
        <h2>Glossary</h2>
        <p className="hint">
          When the source contains the term on the left, the translation is
          expected to use the wording on the right.
        </p>
        <ul className="terms">
          {terms.map((t) => (
            <li key={t.id}>
              <code>{t.source_term}</code> → <code>{t.target_term}</code>
              <button
                className="link"
                onClick={async () => {
                  await api.deleteTerm(projectId, t.id);
                  reload();
                }}
              >
                remove
              </button>
            </li>
          ))}
          {terms.length === 0 && <li className="empty">No terms yet.</li>}
        </ul>
        <form onSubmit={addTerm} className="inline-form">
          <input name="source" placeholder="source term" required />
          <input name="target" placeholder="required translation" required />
          <button type="submit">Add term</button>
        </form>
        <p className="hint">
          Existing translations are not rechecked automatically; open a file and
          press “Recheck file” after changing the glossary.
        </p>
      </section>

      <section>
        <h2>Inconsistent terminology</h2>
        <p className="hint">
          Source strings that have been approved with more than one wording in
          this project.
        </p>
        {clashes.length === 0 ? (
          <p className="empty">Nothing inconsistent.</p>
        ) : (
          <ul className="clashes">
            {clashes.map((c) => (
              <li key={c.source}>
                <div className="source">{c.source}</div>
                {c.variants.map((v) => (
                  <div className="variant" key={v}>
                    {v}
                  </div>
                ))}
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function Progress({ stats }: { stats: LocFile["stats"] }) {
  const pct = (n: number) => (stats.total ? (100 * n) / stats.total : 0);
  return (
    <div className="progress" title={`${stats.approved} of ${stats.total} approved`}>
      <div className="bar">
        <span className="approved" style={{ width: `${pct(stats.approved)}%` }} />
        <span className="review" style={{ width: `${pct(stats.needs_review)}%` }} />
        <span className="draft" style={{ width: `${pct(stats.draft)}%` }} />
      </div>
      <small>
        {stats.approved}/{stats.total} approved
      </small>
    </div>
  );
}
