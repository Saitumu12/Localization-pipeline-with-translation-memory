import { useState } from "react";

import { api } from "../api";
import type { Match, Segment } from "../types";

interface Props {
  segment: Segment;
  reviewer: string;
  selected: boolean;
  onSelect: (on: boolean) => void;
  onSaved: (s: Segment) => void;
}

export function SegmentCard({ segment, reviewer, selected, onSelect, onSaved }: Props) {
  const [target, setTarget] = useState(segment.target_text);
  const [fromMemory, setFromMemory] = useState(false);
  const [matches, setMatches] = useState<Match[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const blocking = segment.issues.some((i) => i.severity === "error");
  const dirty = target !== segment.target_text;

  const save = async (approve: boolean) => {
    setBusy(true);
    setError("");
    try {
      const saved = await api.saveSegment(segment.id, {
        target,
        approve,
        reviewer,
        origin: fromMemory ? "memory" : "human",
      });
      onSaved(saved);
      setTarget(saved.target_text);
      setFromMemory(false);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const loadSuggestions = async () => {
    setMatches([]);
    try {
      setMatches(await api.suggestions(segment.id));
    } catch (err) {
      setError((err as Error).message);
      setMatches(null);
    }
  };

  return (
    <li className={`segment ${blocking ? "has-error" : ""}`}>
      <div className="head">
        <input
          type="checkbox"
          checked={selected}
          onChange={(e) => onSelect(e.target.checked)}
          title="select for machine drafting"
        />
        <span className="context">{segment.context}</span>
        {segment.is_plural && (
          <span className="badge plain">plural form {segment.form_index + 1}</span>
        )}
        {segment.max_width > 0 && (
          <span className="badge plain">max {segment.max_width} chars</span>
        )}
        <div className="spacer" />
        <span className={`badge status-${segment.status}`}>{segment.status}</span>
        <span className={`badge origin-${segment.origin}`}>{segment.origin}</span>
        {segment.reviewed_by && (
          <span className="badge plain">approved by {segment.reviewed_by}</span>
        )}
      </div>

      <div className="source">{segment.source_text}</div>
      {segment.notes && <div className="notes">{segment.notes}</div>}

      <textarea
        value={target}
        rows={2}
        spellCheck={false}
        onChange={(e) => {
          setTarget(e.target.value);
          setFromMemory(false);
        }}
        placeholder="translation"
      />

      {segment.issues.length > 0 && (
        <ul className="issues">
          {segment.issues.map((i, n) => (
            <li key={n} className={i.severity}>
              <strong>{i.kind}</strong> {i.message}
            </li>
          ))}
        </ul>
      )}

      {error && <p className="error">{error}</p>}

      <div className="actions">
        <button onClick={() => save(false)} disabled={busy || !dirty}>
          Save draft
        </button>
        <button
          className="primary"
          onClick={() => save(true)}
          disabled={busy || !reviewer || blocking}
          title={
            !reviewer
              ? "Enter your name in the Reviewer field first"
              : blocking
                ? "Fix the blocking finding before approving"
                : undefined
          }
        >
          Approve
        </button>
        <button onClick={loadSuggestions} disabled={busy}>
          Suggestions
        </button>
      </div>

      {matches !== null && (
        <div className="suggestions">
          {matches.length === 0 ? (
            <p className="empty">No prior translation is close enough.</p>
          ) : (
            matches.map((m) => (
              <button
                key={m.id}
                className="match"
                onClick={() => {
                  setTarget(m.target_text);
                  setFromMemory(true);
                }}
              >
                <span className={`badge match-${m.kind}`}>
                  {m.kind} {Math.round(m.score * 100)}%
                </span>
                <span className="match-target">{m.target_text}</span>
                <span className="match-source">
                  from “{m.source_text}” · approved by {m.approved_by}
                </span>
              </button>
            ))
          )}
        </div>
      )}
    </li>
  );
}
