package media

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	json "encoding/json/v2"
)

func parseDomainFilter(domains []string) (includes, excludes []string) {
	for _, domain := range domains {
		if strings.HasPrefix(domain, "-") {
			excludes = append(excludes, domain[1:])
		} else {
			includes = append(includes, domain)
		}
	}
	return
}

func nativeSearchPayload(provider, target string, input *searchBody) (string, []byte, string, error) {
	if provider == "searxng" {
		return searxngSearchPayload(target, input)
	}
	includes, excludes := parseDomainFilter(input.DomainFilter)
	var payload []byte
	var err error
	if provider == "tavily" {
		topic := "general"
		if input.SearchType == searchNews {
			topic = "news"
		}
		payload, err = json.Marshal(struct {
			Query      string   `json:"query"`
			MaxResults int      `json:"max_results"`
			Topic      string   `json:"topic"`
			Includes   []string `json:"include_domains,omitempty"`
			Excludes   []string `json:"exclude_domains,omitempty"`
			Country    string   `json:"country,omitempty"`
		}{input.Query, input.MaxResults, topic, includes, excludes, input.Country})
	} else {
		category := ""
		if input.SearchType == searchNews {
			category = "news"
		}
		payload, err = json.Marshal(struct {
			Query      string   `json:"query"`
			NumResults int      `json:"numResults"`
			Type       string   `json:"type"`
			Text       bool     `json:"text"`
			Highlights bool     `json:"highlights"`
			Includes   []string `json:"includeDomains,omitempty"`
			Excludes   []string `json:"excludeDomains,omitempty"`
			Category   string   `json:"category,omitempty"`
		}{input.Query, input.MaxResults, "auto", true, true, includes, excludes, category})
	}
	if err != nil {
		return "", nil, "", searchError(http.StatusBadRequest, fmt.Sprintf("encode search request: %v", err))
	}
	return http.MethodPost, payload, target, nil
}

func searxngSearchPayload(target string, input *searchBody) (string, []byte, string, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", nil, "", searchError(http.StatusBadRequest, fmt.Sprintf("parse SearXNG URL: %v", err))
	}
	if !strings.HasSuffix(u.Path, "/search") {
		u.Path = strings.TrimRight(u.Path, "/") + "/search"
	}
	query := u.Query()
	query.Set("q", input.Query)
	query.Set("format", "json")
	category := "general"
	if input.SearchType == searchNews {
		category = "news"
	}
	query.Set("categories", category)
	if input.Language != "" {
		query.Set("language", input.Language)
	}
	if input.TimeRange != "" && input.TimeRange != "any" {
		query.Set("time_range", input.TimeRange)
	}
	if input.Offset > 0 {
		query.Set("pageno", strconv.Itoa(input.Offset/input.MaxResults+1))
	}
	u.RawQuery = query.Encode()
	return http.MethodGet, nil, u.String(), nil
}
