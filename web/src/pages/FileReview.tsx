import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router-dom";

import { api } from "../api";
import { useReviewer } from "../App";
import { SegmentCard } from "../components/SegmentCard";
import type { Segment } from "../types";

const PAGE = 25;

const STATUSES = [
  { value: "", label: "All" },
  { value: "untranslated", label: "Untranslated" },
  { value: "needs_review", label: "Needs review" },
  { value: "draft", label: "Draft" },
  { value: "approved", label: "Approved" },
];

export function FileReview() {
  const fileId = Number(useParams().fileId);
  const [reviewer] = useReviewer();

  const [segments, setSegments] = useState<Segment[]>([]);
  const [total, setTotal] = useState(0);
  const [status, setStatus] = useState("");
  const [search, setSearch] = useState("");
  const [onlyIssues, setOnlyIssues] = useState(false);
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<number[]>([]);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const res = await api.segments(fileId, {
        status,
        search,
        issues: onlyIssues,
        limit: PAGE,
        offset: page * PAGE,
      });
      setSegments(res.segments);
      setTotal(res.total);
      setError("");
    } catch (err) {
      setError((err as Error).message);
    }
  }, [fileId, status, search, onlyIssues, page]);

  useEffect(() => {
    load();
  }, [load]);

  // Replace one row in place so editing a segment does not reshuffle the list
  // out from under the reviewer.
  const replace = (updated: Segment) =>
    setSegments((prev) => prev.map((s) => (s.id === updated.id ? updated : s)));

  const pretranslate = async () => {
    if (selected.length === 0) return;
    setMessage("Asking the model…");
    setError("");
    try {
      const res = await api.pretranslate(selected);
      setMessage(
        `${res.model}: ${res.translated} of ${res.requested} drafted, ` +
          `${res.with_issues} with QA findings. All of them need review.`,
      );
      setSelected([]);
      load();
    } catch (err) {
      setError((err as Error).message);
      setMessage("");
    }
  };

  const recheck = async () => {
    setMessage("Rechecking…");
    try {
      const res = await api.recheck(fileId);
      setMessage(`${res.issues} findings across the file.`);
      load();
    } catch (err) {
      setError((err as Error).message);
      setMessage("");
    }
  };

  const pages = Math.ceil(total / PAGE);

  return (
    <div className="page">
      <div className="toolbar">
        <select
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
            setPage(0);
          }}
        >
          {STATUSES.map((s) => (
            <option key={s.value} value={s.value}>
              {s.label}
            </option>
          ))}
        </select>

        <input
          value={search}
          placeholder="search source text"
          onChange={(e) => {
            setSearch(e.target.value);
            setPage(0);
          }}
        />

        <label className="checkbox">
          <input
            type="checkbox"
            checked={onlyIssues}
            onChange={(e) => {
              setOnlyIssues(e.target.checked);
              setPage(0);
            }}
          />
          only with findings
        </label>

        <div className="spacer" />

        <button onClick={recheck}>Recheck file</button>
        <button onClick={pretranslate} disabled={selected.length === 0}>
          Draft {selected.length || ""} with the model
        </button>
      </div>

      {error && <p className="error">{error}</p>}
      {message && <p className="busy">{message}</p>}

      <p className="count">
        {total} segments{status ? ` with status ${status}` : ""}
      </p>

      <ul className="segments">
        {segments.map((s) => (
          <SegmentCard
            key={s.id}
            segment={s}
            reviewer={reviewer}
            selected={selected.includes(s.id)}
            onSelect={(on) =>
              setSelected((prev) =>
                on ? [...prev, s.id] : prev.filter((id) => id !== s.id),
              )
            }
            onSaved={replace}
          />
        ))}
        {segments.length === 0 && <li className="empty">Nothing here.</li>}
      </ul>

      {pages > 1 && (
        <div className="pager">
          <button disabled={page === 0} onClick={() => setPage(page - 1)}>
            Previous
          </button>
          <span>
            page {page + 1} of {pages}
          </span>
          <button disabled={page + 1 >= pages} onClick={() => setPage(page + 1)}>
            Next
          </button>
        </div>
      )}
    </div>
  );
}
