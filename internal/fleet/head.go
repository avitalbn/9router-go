package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func forkHead(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+Repository+"/commits/main", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fork HEAD check HTTP %d", resp.StatusCode)
	}
	var head struct {
		SHA string `json:"sha"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&head); err != nil {
		return "", err
	}
	if len(head.SHA) != 40 {
		return "", fmt.Errorf("invalid fork HEAD response")
	}
	return head.SHA, nil
}
