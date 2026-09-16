// Command locctl drives the pipeline from the terminal: import files, sign them
// off, run the checks, query the memory and write files back out.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/Saitumu12/localization-pipeline/internal/config"
	"github.com/Saitumu12/localization-pipeline/internal/embed"
	"github.com/Saitumu12/localization-pipeline/internal/format"
	"github.com/Saitumu12/localization-pipeline/internal/llm"
	"github.com/Saitumu12/localization-pipeline/internal/pipeline"
	"github.com/Saitumu12/localization-pipeline/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "projects":
		err = cmdProjects(args)
	case "new-project":
		err = cmdNewProject(args)
	case "import":
		err = cmdImport(args)
	case "files":
		err = cmdFiles(args)
	case "adopt":
		err = cmdAdopt(args)
	case "recheck":
		err = cmdRecheck(args)
	case "issues":
		err = cmdIssues(args)
	case "suggest":
		err = cmdSuggest(args)
	case "export":
		err = cmdExport(args)
	case "inconsistencies":
		err = cmdInconsistencies(args)
	case "glossary":
		err = cmdGlossary(args)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `locctl <command> [flags]

  projects                                  list projects
  new-project  -name -source -target        create a project
  import       -project ID FILE...          import localization files
  files        -project ID                  list files with review progress
  adopt        -file ID -reviewer NAME      sign off the translations a file arrived with
  recheck      -file ID                     rerun the checks, e.g. after a glossary change
  issues       -file ID [-kind K]           list the QA findings for a file
  suggest      -project ID -text "..."      query the translation memory
  export       -file ID [-approved-only] [-o FILE]
  inconsistencies -project ID               sources approved with more than one wording
  glossary     -project ID [-add-source S -add-target T]
`)
}

func open(ctx context.Context) (*pipeline.Pipeline, func(), error) {
	cfg := config.FromEnv()
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, nil, err
	}
	em := embed.New(cfg.EmbeddingsURL)
	if err := em.Probe(ctx, config.EmbeddingDimensions); err != nil {
		st.Close()
		return nil, nil, err
	}
	mt := llm.New(cfg.AnthropicKey, cfg.LLMModel).WithEndpoint(cfg.AnthropicBaseURL)
	return pipeline.New(st, em, mt), st.Close, nil
}

func cmdProjects(args []string) error {
	fs := flag.NewFlagSet("projects", flag.ExitOnError)
	_ = fs.Parse(args)

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	ps, err := p.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	tw := table()
	fmt.Fprintln(tw, "ID\tNAME\tSOURCE\tTARGET\tMEMORY")
	for _, pr := range ps {
		n, _ := p.Store.CountTMEntries(ctx, pr.ID)
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\n", pr.ID, pr.Name, pr.SourceLocale, pr.TargetLocale, n)
	}
	return tw.Flush()
}

func cmdNewProject(args []string) error {
	fs := flag.NewFlagSet("new-project", flag.ExitOnError)
	name := fs.String("name", "", "project name")
	source := fs.String("source", "en", "source locale")
	target := fs.String("target", "", "target locale")
	_ = fs.Parse(args)
	if *name == "" || *target == "" {
		return fmt.Errorf("-name and -target are required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	pr, err := p.Store.CreateProject(ctx, store.Project{
		Name: *name, SourceLocale: *source, TargetLocale: *target,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created project %d (%s, %s -> %s)\n", pr.ID, pr.Name, pr.SourceLocale, pr.TargetLocale)
	return nil
}

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	project := fs.Int64("project", 0, "project id")
	_ = fs.Parse(args)
	if *project == 0 || fs.NArg() == 0 {
		return fmt.Errorf("-project ID and at least one file are required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	for _, path := range fs.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		res, err := p.Import(ctx, *project, filepath.Base(path), data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Printf("%s: file %d, %d segments (%d translated, %d untranslated, %d skipped)\n",
			res.File.Name, res.File.ID, res.Segments, res.Translated, res.Untranslated, res.Skipped)
		if res.KeptReview > 0 {
			fmt.Printf("  %d entries came back unchanged and kept their review state\n", res.KeptReview)
		}
	}
	return nil
}

func cmdFiles(args []string) error {
	fs := flag.NewFlagSet("files", flag.ExitOnError)
	project := fs.Int64("project", 0, "project id")
	_ = fs.Parse(args)
	if *project == 0 {
		return fmt.Errorf("-project is required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	files, err := p.Store.ListFiles(ctx, *project)
	if err != nil {
		return err
	}
	tw := table()
	fmt.Fprintln(tw, "ID\tNAME\tFORMAT\tTOTAL\tAPPROVED\tREVIEW\tDRAFT\tEMPTY\tERRORS\tWARNINGS")
	for _, f := range files {
		st, err := p.Store.StatsFor(ctx, f.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			f.ID, f.Name, f.Format, st.Total, st.Approved, st.NeedsReview, st.Draft, st.Untranslated, st.Errors, st.Warnings)
	}
	return tw.Flush()
}

func cmdAdopt(args []string) error {
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	file := fs.Int64("file", 0, "file id")
	reviewer := fs.String("reviewer", "", "name of the person signing these translations off")
	_ = fs.Parse(args)
	if *file == 0 || *reviewer == "" {
		return fmt.Errorf("-file and -reviewer are required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	res, err := p.AdoptImported(ctx, *file, *reviewer)
	if err != nil {
		return err
	}
	fmt.Printf("considered %d, approved %d as %q, blocked by QA issues %d, added to memory %d\n",
		res.Considered, res.Approved, *reviewer, res.Blocked, res.Indexed)
	return nil
}

func cmdRecheck(args []string) error {
	fs := flag.NewFlagSet("recheck", flag.ExitOnError)
	file := fs.Int64("file", 0, "file id")
	_ = fs.Parse(args)
	if *file == 0 {
		return fmt.Errorf("-file is required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	n, err := p.Recheck(ctx, *file)
	if err != nil {
		return err
	}
	fmt.Printf("%d findings\n", n)
	return nil
}

func cmdIssues(args []string) error {
	fs := flag.NewFlagSet("issues", flag.ExitOnError)
	file := fs.Int64("file", 0, "file id")
	kind := fs.String("kind", "", "only this kind: placeholder, length, terminology, inconsistency")
	limit := fs.Int("limit", 50, "maximum findings to print")
	_ = fs.Parse(args)
	if *file == 0 {
		return fmt.Errorf("-file is required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	// Page through every flagged segment so the totals cover the whole file and
	// not just the first response.
	shown, counts := 0, map[string]int{}
	for offset := 0; ; {
		segs, total, err := p.Store.ListSegments(ctx, *file, store.SegmentFilter{
			OnlyIssue: true, Limit: 500, Offset: offset,
		})
		if err != nil {
			return err
		}
		if len(segs) == 0 {
			break
		}
		for _, s := range segs {
			for _, is := range s.Issues {
				counts[string(is.Kind)]++
				if *kind != "" && string(is.Kind) != *kind {
					continue
				}
				if shown >= *limit {
					continue
				}
				shown++
				fmt.Printf("[%s/%s] %s\n  source: %s\n  target: %s\n  %s\n\n",
					is.Kind, is.Severity, s.Context,
					format.PlainText(s.SourceText), format.PlainText(s.TargetText), is.Message)
			}
		}
		offset += len(segs)
		if offset >= total {
			break
		}
	}

	seen := make([]string, 0, len(counts))
	for k := range counts {
		seen = append(seen, k)
	}
	sort.Strings(seen)
	fmt.Print("totals:")
	for _, k := range seen {
		fmt.Printf(" %s=%d", k, counts[k])
	}
	fmt.Println()
	return nil
}

func cmdSuggest(args []string) error {
	fs := flag.NewFlagSet("suggest", flag.ExitOnError)
	project := fs.Int64("project", 0, "project id")
	text := fs.String("text", "", "source string to look up")
	limit := fs.Int("limit", 5, "maximum suggestions")
	minScore := fs.Float64("min-score", pipeline.DefaultMinScore, "minimum cosine similarity")
	_ = fs.Parse(args)
	if *project == 0 || *text == "" {
		return fmt.Errorf("-project and -text are required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	matches, err := p.SearchMemory(ctx, *project, *text, *limit, *minScore)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Println("no suggestions above the similarity threshold")
		return nil
	}
	// Show the text the way a translator reads it. The HTTP API returns the raw
	// fragment instead, because the editor inserts it verbatim.
	for _, m := range matches {
		fmt.Printf("%-8s %.3f  %s\n         -> %s   (approved by %s)\n",
			m.Kind, m.Score, format.PlainText(m.SourceText), format.PlainText(m.TargetText), m.ApprovedBy)
	}
	return nil
}

func cmdExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	file := fs.Int64("file", 0, "file id")
	approvedOnly := fs.Bool("approved-only", false, "leave anything not signed off exactly as it was")
	out := fs.String("o", "", "write to this path instead of stdout")
	_ = fs.Parse(args)
	if *file == 0 {
		return fmt.Errorf("-file is required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	data, name, err := p.Export(ctx, *file, *approvedOnly)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	target := *out
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		target = filepath.Join(target, name)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", target, len(data))
	return nil
}

func cmdInconsistencies(args []string) error {
	fs := flag.NewFlagSet("inconsistencies", flag.ExitOnError)
	project := fs.Int64("project", 0, "project id")
	_ = fs.Parse(args)
	if *project == 0 {
		return fmt.Errorf("-project is required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	rows, err := p.Store.InconsistentSources(ctx, *project, 200)
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Printf("%s\n", format.PlainText(r.Source))
		for _, v := range r.Variants {
			fmt.Printf("    %s\n", format.PlainText(v))
		}
	}
	fmt.Printf("%d source strings approved with more than one wording\n", len(rows))
	return nil
}

func cmdGlossary(args []string) error {
	fs := flag.NewFlagSet("glossary", flag.ExitOnError)
	project := fs.Int64("project", 0, "project id")
	addSource := fs.String("add-source", "", "source term to add")
	addTarget := fs.String("add-target", "", "required translation of that term")
	_ = fs.Parse(args)
	if *project == 0 {
		return fmt.Errorf("-project is required")
	}

	ctx := context.Background()
	p, closeFn, err := open(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	if *addSource != "" {
		if *addTarget == "" {
			return fmt.Errorf("-add-target is required with -add-source")
		}
		if _, err := p.Store.AddGlossaryTerm(ctx, store.GlossaryTerm{
			ProjectID: *project, SourceTerm: *addSource, TargetTerm: *addTarget,
		}); err != nil {
			return err
		}
		fmt.Printf("added %q -> %q\n", *addSource, *addTarget)
	}

	terms, err := p.Store.Glossary(ctx, *project)
	if err != nil {
		return err
	}
	tw := table()
	fmt.Fprintln(tw, "ID\tSOURCE\tTARGET\tCASE")
	for _, t := range terms {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%v\n", t.ID, t.SourceTerm, t.TargetTerm, t.CaseSensitive)
	}
	return tw.Flush()
}

func table() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}
