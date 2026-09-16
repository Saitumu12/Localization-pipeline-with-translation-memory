export type Status =
  | "untranslated"
  | "draft"
  | "needs_review"
  | "approved"
  | "rejected";

export type Origin = "import" | "human" | "machine" | "memory";

export interface Project {
  id: number;
  name: string;
  source_locale: string;
  target_locale: string;
  length_tolerance: number;
}

export interface FileStats {
  total: number;
  approved: number;
  needs_review: number;
  draft: number;
  untranslated: number;
  rejected: number;
  errors: number;
  warnings: number;
}

export interface LocFile {
  id: number;
  project_id: number;
  name: string;
  format: "xliff" | "qtts";
  sha256: string;
  imported_at: string;
  stats: FileStats;
}

export interface Issue {
  kind: "placeholder" | "length" | "terminology" | "inconsistency";
  severity: "error" | "warning";
  message: string;
}

export interface Segment {
  id: number;
  file_id: number;
  unit_key: string;
  form_index: number;
  context: string;
  source_text: string;
  target_text: string;
  status: Status;
  origin: Origin;
  is_plural: boolean;
  max_width: number;
  notes: string;
  reviewed_by: string | null;
  reviewed_at: string | null;
  issues: Issue[];
}

export interface Match {
  id: number;
  source_text: string;
  target_text: string;
  approved_by: string;
  created_at: string;
  kind: "exact" | "semantic";
  score: number;
}

export interface GlossaryTerm {
  id: number;
  project_id: number;
  source_term: string;
  target_term: string;
  case_sensitive: boolean;
}

export interface Inconsistency {
  source: string;
  variants: string[];
}

export interface Health {
  status: string;
  embeddings: boolean;
  embedding_model: string;
  machine_translation: boolean;
  llm_model: string;
}
