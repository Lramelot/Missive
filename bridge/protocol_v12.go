package main

const (
	EventExportProgress    = "export_progress"
	EventSaveMediaProgress = "save_media_progress"

	ExportHTML     = "html"
	ExportMarkdown = "markdown"
	ExportJSON     = "json"
)

type ExportThreadParams struct {
	ThreadID     string `json:"thread_id"`
	Dir          string `json:"dir"`
	Format       string `json:"format"`
	IncludeMedia bool   `json:"include_media"`
	Locale       string `json:"locale,omitempty"`
}

type ExportThreadResult struct {
	Path        string `json:"path"`
	Messages    int    `json:"messages"`
	Media       int    `json:"media"`
	MediaFailed int    `json:"media_failed"`
}

type ExportProgressEvent struct {
	ThreadID string `json:"thread_id"`
	Messages int    `json:"messages"`
	Media    int    `json:"media"`
}

type SaveMediaParams struct {
	ThreadID string   `json:"thread_id"`
	Dir      string   `json:"dir"`
	Kinds    []string `json:"kinds"`
	Locale   string   `json:"locale,omitempty"`
}

type SaveMediaResult struct {
	Saved   int    `json:"saved"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
	Dir     string `json:"dir"`
}

type SaveMediaProgressEvent struct {
	ThreadID string `json:"thread_id"`
	Saved    int    `json:"saved"`
	Total    *int   `json:"total,omitempty"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
}

type CancelArchiveResult struct {
	Cancelled bool `json:"cancelled"`
}

type exportedMessage struct {
	Message
	LocalFiles map[string]string `json:"local_files,omitempty"`
}
