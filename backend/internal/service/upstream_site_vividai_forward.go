package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type siteAcceptedVideoAccountKey struct{}

// The create gate covers balance -> first job receipt. Upstream running is
// authoritative after that receipt, including videos with no active HTTP caller.
// Negative IDs reserve a namespace separate from database account IDs.
func vividAICreateGateID(account *Account) int64 {
	origin := strings.TrimSuffix(strings.TrimRight(account.GetCredential("base_url"), "/"), "/v1")
	sum := sha256.Sum256([]byte(origin + "\x00" + account.GetCredential("api_key")))
	id := int64(binary.BigEndian.Uint64(sum[:8]) & math.MaxInt64)
	if id == 0 {
		id = 1
	}
	return -id
}

func (s *OpenAIGatewayService) acquireVividAICreate(ctx context.Context, account *Account) (func(), error) {
	if s.concurrencyService == nil {
		return nil, errors.New("VividAI shared concurrency service unavailable")
	}
	gateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var release func()
	for {
		slot, err := s.concurrencyService.AcquireAccountSlot(gateCtx, vividAICreateGateID(account), 1)
		if err != nil {
			return nil, errors.New("VividAI shared concurrency check failed")
		}
		if slot.Acquired {
			release = slot.ReleaseFunc
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-gateCtx.Done():
			timer.Stop()
			return nil, &vividAIError{Status: 429, Code: "4293", Message: "VividAI is accepting another task; retry later", Rejected: true}
		case <-timer.C:
		}
	}
	failed := true
	defer func() {
		if failed {
			release()
		}
	}()
	req, err := http.NewRequestWithContext(gateCtx, http.MethodGet, buildOpenAIEndpointURL(account.GetOpenAIBaseURL(), "/v1/balance"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return nil, errors.New("VividAI concurrency balance request failed")
	}
	defer resp.Body.Close()
	var balance struct {
		Object  string `json:"object"`
		Running *int64 `json:"running"`
		Limit   *int64 `json:"concurrency_limit"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&balance) != nil || balance.Object != "user.balance" || balance.Running == nil || balance.Limit == nil || *balance.Running < 0 || *balance.Limit <= 0 {
		return nil, errors.New("VividAI concurrency balance response invalid")
	}
	if *balance.Running >= *balance.Limit {
		return nil, &vividAIError{Status: 429, Code: "4293", Message: "VividAI upstream account concurrency limit reached", Rejected: true}
	}
	failed = false
	return release, nil
}

type vividAIReceiptBody struct {
	io.Reader
	io.Closer
}

// Read only through the first receipt before releasing the gate. Preserve all
// bytes for the ordinary stream parser, including any terminal first frame.
func vividAIReadReceipt(resp *http.Response) error {
	reader := bufio.NewReader(resp.Body)
	prefix := []byte{}
	line := []byte{}
	for {
		fragment, err := reader.ReadSlice('\n')
		prefix = append(prefix, fragment...)
		line = append(line, fragment...)
		if len(prefix) > 1<<20 {
			return errors.New("VividAI initial receipt too large")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			if err != nil {
				return io.ErrUnexpectedEOF
			}
			line = line[:0]
			continue
		}
		var frame vividAIFrame
		if json.Unmarshal(bytes.TrimSpace(line), &frame) != nil || frame.JobID == "" || (frame.Status != "running" && frame.Status != "succeeded" && frame.Status != "failed") {
			return errors.New("VividAI initial receipt invalid")
		}
		resp.Body = &vividAIReceiptBody{io.MultiReader(bytes.NewReader(prefix), reader), resp.Body}
		return nil
	}
}

func WithSiteImageQuality(ctx context.Context, size, quality string) context.Context {
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	image, err := vividAIImageRequest(&OpenAIImagesRequest{N: 1, Prompt: "price", Size: size, Quality: quality}, "price")
	request.VividAITier = "unknown"
	if err == nil {
		request.VividAITier = image.Quality
	}
	return context.WithValue(ctx, siteRequestKey{}, request)
}
