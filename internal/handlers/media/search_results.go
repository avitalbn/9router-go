package media

import (
	"strings"
	"time"
)

// The dedicated vendors share a results array; their named fields stay typed.
type nativeSearchItem struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Content       string   `json:"content"`
	RawContent    string   `json:"raw_content"`
	Score         *float64 `json:"score"`
	PublishedDate string   `json:"publishedDate"`
	PublishedAt   string   `json:"published_date"`
	Author        string   `json:"author"`
	Text          string   `json:"text"`
	Highlights    []string `json:"highlights"`
	Favicon       string   `json:"favicon"`
	Image         string   `json:"image"`
	Snippet       string   `json:"snippet"`
	Engines       []string `json:"engines"`
	Engine        string   `json:"engine"`
	Category      string   `json:"category"`
	Thumbnail     string   `json:"thumbnail"`
	ImgSrc        string   `json:"img_src"`
}

type searchResultContent struct {
	Format string `json:"format"`
	Text   string `json:"text"`
	Length int    `json:"length"`
}

// Extend the existing envelope only for dedicated APIs; Antigravity and Xquik
// retain their upstream response fields and omission rules.
type nativeSearchResponse struct {
	SearchResponse
	Results []nativeSearchResult `json:"results"`
	Answer  *SearchAnswer        `json:"answer"`
}

type nativeSearchResult struct {
	SearchResult
	DisplayURL string               `json:"display_url,omitempty"`
	Content    *searchResultContent `json:"content"`
}

func buildDisplayURL(raw string) string {
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	raw = strings.TrimPrefix(raw, "www.")
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func searchNullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// JavaScript wire lengths count UTF-16 code units, not UTF-8 bytes.
func searchTextLength(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}

func searchTextPrefix(text string, limit int) string {
	length := 0
	for i, r := range text {
		length++
		if r > 0xffff {
			length++
		}
		if length > limit {
			return text[:i]
		}
	}
	return text
}

func nativeSearchContent(text string) *searchResultContent {
	if text == "" {
		return nil
	}
	return &searchResultContent{Format: "text", Text: text, Length: searchTextLength(text)}
}

func normalizeNativeSearchResults(provider string, items []nativeSearchItem) []nativeSearchResult {
	results := make([]nativeSearchResult, len(items))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := range items {
		item := &items[i]
		result := &results[i]
		result.SearchResult = SearchResult{
			Title: item.Title, URL: item.URL, Position: i + 1,
			Metadata: map[string]any{"author": nil, "language": nil, "source_type": nil, "image_url": nil},
			Citation: SearchCitation{Provider: provider, RetrievedAt: now, Rank: i + 1},
		}
		result.DisplayURL = buildDisplayURL(item.URL)
		populateNativeSearchResult(provider, item, result)
	}
	return results
}

func populateNativeSearchResult(provider string, item *nativeSearchItem, result *nativeSearchResult) {
	if provider != "searxng" && item.Score != nil {
		result.Score = min(1.0, max(0.0, *item.Score))
	}
	switch provider {
	case "tavily":
		result.Snippet = item.Content
		result.Content = nativeSearchContent(item.RawContent)
		result.PublishedAt = searchNullable(item.PublishedAt)
	case "exa":
		if len(item.Highlights) > 0 && item.Highlights[0] != "" {
			result.Snippet = item.Highlights[0]
		} else {
			result.Snippet = searchTextPrefix(item.Text, 300)
		}
		result.Content = nativeSearchContent(item.Text)
		result.PublishedAt = searchNullable(item.PublishedDate)
		result.FaviconURL = searchNullable(item.Favicon)
		result.Metadata["author"] = searchNullable(item.Author)
		result.Metadata["image_url"] = searchNullable(item.Image)
	case "searxng":
		populateSearxngResult(item, result)
	}
}

func populateSearxngResult(item *nativeSearchItem, result *nativeSearchResult) {
	result.Snippet = item.Content
	if result.Snippet == "" {
		result.Snippet = item.Snippet
	}
	published := item.PublishedDate
	if published == "" {
		published = item.PublishedAt
	}
	result.PublishedAt = searchNullable(published)
	source := item.Engine
	if source == "" {
		source = item.Category
	}
	if len(item.Engines) > 0 {
		source = strings.Join(item.Engines, ", ")
	}
	result.Metadata["source_type"] = searchNullable(source)
	image := item.Thumbnail
	if image == "" {
		image = item.ImgSrc
	}
	result.Metadata["image_url"] = searchNullable(image)
}
