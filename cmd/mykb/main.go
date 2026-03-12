package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/internal/cliconfig"
	"mykb/internal/tui"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ingest":
		runIngest(os.Args[2:])
	case "query":
		runQuery(os.Args[2:])
	default:
		// Default to query: "mykb <query>" is shorthand for "mykb query <query>"
		runQuery(os.Args[1:])
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  mykb <query> [flags]                  (shorthand for mykb query)")
	fmt.Fprintln(os.Stderr, "  mykb query <query> [--host HOST] [--top-k N] [--vector-depth N] [--fts-depth N] [--rerank-depth N] [--no-merge]")
	fmt.Fprintln(os.Stderr, "  mykb ingest <url> [--quiet] [--force] [--host HOST]")
}

func connect(host string) (*grpc.ClientConn, error) {
	return grpc.NewClient(host, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// --- ingest command ---

func runIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "suppress progress, print ok/error only")
	force := fs.Bool("force", false, "re-ingest even if URL already exists")
	host := fs.String("host", "", "server address (default: from config)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb ingest <url> [--quiet] [--force] [--host HOST]")
		os.Exit(1)
	}
	url := fs.Arg(0)

	cfg := cliconfig.Load("")
	if *host != "" {
		cfg.Host = *host
	}

	conn, err := connect(cfg.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := mykbv1.NewKBServiceClient(conn)
	stream, err := client.IngestURL(context.Background(), &mykbv1.IngestURLRequest{Url: url, Force: *force})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var lastStatus string
	var stepStart time.Time
	var progress strings.Builder

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if *quiet {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
			}
			os.Exit(1)
		}

		if *quiet {
			continue
		}

		status := msg.GetStatus()
		if status != lastStatus {
			if lastStatus != "" {
				elapsed := time.Since(stepStart)
				fmt.Fprintf(&progress, "(%.1fs)", elapsed.Seconds())
			}
			fmt.Fprintf(&progress, "..%s..", status)
			lastStatus = status
			stepStart = time.Now()
		}

		fmt.Fprintf(os.Stderr, "\r%s", progress.String())
	}

	if *quiet {
		fmt.Println("ok")
	} else {
		if lastStatus != "" {
			elapsed := time.Since(stepStart)
			fmt.Fprintf(&progress, "(%.1fs)", elapsed.Seconds())
		}
		fmt.Fprintf(os.Stderr, "\r%s done.\n", progress.String())
	}
}

// --- query command ---

func runQuery(args []string) {
	fs := flag.NewFlagSet("query", flag.ExitOnError)
	host := fs.String("host", "", "server address (default: from config)")
	lines := fs.Int("lines", 0, "chunk text preview lines")
	topK := fs.Int("top-k", 0, "number of results to display")
	vectorDepth := fs.Int("vector-depth", 0, "candidates from Qdrant")
	ftsDepth := fs.Int("fts-depth", 0, "candidates from Meilisearch")
	rerankDepth := fs.Int("rerank-depth", 0, "candidates sent to reranker")
	noMerge := fs.Bool("no-merge", false, "return individual chunks instead of merged segments")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: mykb query <query> [flags]")
		os.Exit(1)
	}
	query := strings.Join(fs.Args(), " ")

	cfg := cliconfig.Load("")
	if *host != "" {
		cfg.Host = *host
	}
	if *lines > 0 {
		cfg.Lines = *lines
	}
	if *topK > 0 {
		cfg.TopK = *topK
	}
	if *vectorDepth > 0 {
		cfg.VectorDepth = *vectorDepth
	}
	if *ftsDepth > 0 {
		cfg.FTSDepth = *ftsDepth
	}
	if *rerankDepth > 0 {
		cfg.RerankDepth = *rerankDepth
	}

	conn, err := connect(cfg.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := mykbv1.NewKBServiceClient(conn)

	resp, err := client.Query(context.Background(), &mykbv1.QueryRequest{
		Query:       query,
		TopK:        int32(cfg.TopK),
		VectorDepth: int32(cfg.VectorDepth),
		FtsDepth:    int32(cfg.FTSDepth),
		RerankDepth: int32(cfg.RerankDepth),
		NoMerge:     *noMerge,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(resp.Results) == 0 {
		fmt.Println("no results")
		return
	}

	// Resolve document metadata (title, URL) via GetDocuments
	docIDs := uniqueDocIDs(resp.Results)
	docsResp, err := client.GetDocuments(context.Background(), &mykbv1.GetDocumentsRequest{
		Ids: docIDs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching documents: %v\n", err)
		os.Exit(1)
	}
	docMap := make(map[string]*mykbv1.Document, len(docsResp.Documents))
	for _, doc := range docsResp.Documents {
		docMap[doc.Id] = doc
	}

	// In merge mode, deduplicate by document (keep highest-scoring segment only).
	results := resp.Results
	if !*noMerge {
		results = deduplicateByDocument(results)
	}

	// Build TUI result items from query results + document metadata.
	items := make([]tui.ResultItem, len(results))
	for i, r := range results {
		doc := docMap[r.DocumentId]
		item := tui.ResultItem{
			Rank:          i + 1,
			Score:         r.Score,
			Title:         r.DocumentId,
			ChunkIndex:    int(r.ChunkIndex),
			ChunkIndexEnd: int(r.ChunkIndexEnd),
			Text:          r.Text,
		}
		if doc != nil {
			if doc.Title != "" {
				item.Title = doc.Title
			}
			item.URL = doc.Url
			item.ChunkCount = int(doc.ChunkCount)
			if doc.CreatedAt != 0 {
				item.CreatedAt = time.Unix(doc.CreatedAt, 0)
			}
			if doc.UpdatedAt != 0 {
				item.UpdatedAt = time.Unix(doc.UpdatedAt, 0)
			}
		}
		items[i] = item
	}

	// Launch TUI if stdout is a terminal and NO_COLOR is not set; otherwise plain text.
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))
	_, noColor := os.LookupEnv("NO_COLOR")
	if isTTY && !noColor {
		p := tea.NewProgram(tui.New(items), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	} else {
		printPlainResults(items, getTerminalWidth())
	}
}

func printPlainResults(items []tui.ResultItem, termWidth int) {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(termWidth),
	)
	if err != nil {
		renderer = nil
	}

	for i, item := range items {
		// Header: # #N {score} Title  chunk_pos
		chunkPos := item.ChunkPosition()
		if chunkPos != "" {
			chunkPos = "  " + chunkPos
		}
		fmt.Printf("# #%d {%.3f} %s%s\n", item.Rank, item.Score, item.Title, chunkPos)

		// URL on next line
		if item.URL != "" {
			fmt.Println(item.URL)
		}

		// Dates
		var dates []string
		if !item.CreatedAt.IsZero() {
			dates = append(dates, "Added "+item.CreatedAt.Format("2006-01-02"))
		}
		if !item.UpdatedAt.IsZero() {
			dates = append(dates, "Ingested "+item.UpdatedAt.Format("2006-01-02"))
		}
		if len(dates) > 0 {
			fmt.Println(strings.Join(dates, "  "))
		}

		// Blank line, then glamour-rendered markdown body
		if item.Text != "" {
			fmt.Println()
			if renderer != nil {
				rendered, err := renderer.Render(item.Text)
				if err == nil {
					fmt.Print(rendered)
				} else {
					fmt.Println(item.Text)
				}
			} else {
				fmt.Println(item.Text)
			}
		}

		// Separator between results (not after last)
		if i < len(items)-1 {
			fmt.Println("---")
		}
	}
}

func deduplicateByDocument(results []*mykbv1.QueryResult) []*mykbv1.QueryResult {
	seen := make(map[string]bool)
	var deduped []*mykbv1.QueryResult
	for _, r := range results {
		if !seen[r.DocumentId] {
			seen[r.DocumentId] = true
			deduped = append(deduped, r)
		}
	}
	return deduped
}

func uniqueDocIDs(results []*mykbv1.QueryResult) []string {
	seen := make(map[string]bool)
	var ids []string
	for _, r := range results {
		if !seen[r.DocumentId] {
			seen[r.DocumentId] = true
			ids = append(ids, r.DocumentId)
		}
	}
	return ids
}

func getTerminalWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}
