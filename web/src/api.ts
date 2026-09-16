import type {
  GlossaryTerm,
  Health,
  Inconsistency,
  LocFile,
  Match,
  Project,
  Segment,
} from "./types";

// The Go server answers errors as {"error": "..."} with a 4xx, so unwrap that
// into a real Error rather than letting a failed request look like success.
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  const text = await res.text();
  let body: unknown = null;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      body = null;
    }
  }
  if (!res.ok) {
    const message =
      body && typeof body === "object" && "error" in body
        ? String((body as { error: unknown }).error)
        : `${res.status} ${res.statusText}`;
    throw new Error(message);
  }
  return body as T;
}

function json(method: string, payload: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  };
}

export const api = {
  health: () => request<Health>("/api/health"),

  projects: () => request<Project[]>("/api/projects"),
  project: (id: number) => request<Project>(`/api/projects/${id}`),
  createProject: (p: {
    name: string;
    source_locale: string;
    target_locale: string;
  }) => request<Project>("/api/projects", json("POST", p)),

  files: (projectId: number) =>
    request<LocFile[]>(`/api/projects/${projectId}/files`),

  upload: (projectId: number, file: File) => {
    const form = new FormData();
    form.append("file", file);
    return request<{ file: LocFile; segments: number }>(
      `/api/projects/${projectId}/files`,
      { method: "POST", body: form },
    );
  },

  segments: (
    fileId: number,
    params: {
      status?: string;
      search?: string;
      issues?: boolean;
      limit?: number;
      offset?: number;
    },
  ) => {
    const q = new URLSearchParams();
    if (params.status) q.set("status", params.status);
    if (params.search) q.set("search", params.search);
    if (params.issues) q.set("issues", "1");
    q.set("limit", String(params.limit ?? 25));
    q.set("offset", String(params.offset ?? 0));
    return request<{ segments: Segment[]; total: number }>(
      `/api/files/${fileId}/segments?${q}`,
    );
  },

  saveSegment: (
    id: number,
    body: { target: string; approve: boolean; reviewer: string; origin?: string },
  ) => request<Segment>(`/api/segments/${id}`, json("PUT", body)),

  suggestions: (id: number) =>
    request<Match[]>(`/api/segments/${id}/suggestions`),

  pretranslate: (segmentIds: number[]) =>
    request<{
      model: string;
      requested: number;
      translated: number;
      with_issues: number;
    }>("/api/segments/pretranslate", json("POST", { segment_ids: segmentIds })),

  recheck: (fileId: number) =>
    request<{ issues: number }>(`/api/files/${fileId}/recheck`, {
      method: "POST",
    }),

  adopt: (fileId: number, reviewer: string) =>
    request<{ considered: number; approved: number; blocked: number; indexed: number }>(
      `/api/files/${fileId}/adopt`,
      json("POST", { reviewer }),
    ),

  glossary: (projectId: number) =>
    request<GlossaryTerm[]>(`/api/projects/${projectId}/glossary`),

  addTerm: (projectId: number, term: { source_term: string; target_term: string }) =>
    request<GlossaryTerm>(`/api/projects/${projectId}/glossary`, json("POST", term)),

  deleteTerm: (projectId: number, termId: number) =>
    fetch(`/api/projects/${projectId}/glossary/${termId}`, { method: "DELETE" }),

  inconsistencies: (projectId: number) =>
    request<Inconsistency[]>(`/api/projects/${projectId}/inconsistencies`),

  exportURL: (fileId: number, approvedOnly: boolean) =>
    `/api/files/${fileId}/export${approvedOnly ? "?approved_only=1" : ""}`,
};
