package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/eigen-hq/cell-platform/internal/cell"
)

// The model provider (E-80): an admin sets it, the API checks the key
// against the provider and writes it; the operator configures the gateway.
// The API never reads the key back.

func (s *Server) getModels(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	v, _, err := s.models(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// models reads what the operator tells about the provider, and the version
// of the settings it told it from; until it told anything, there is none.
func (s *Server) models(ctx context.Context) (cell.ModelsView, string, error) {
	var v cell.ModelsView
	var cm corev1.ConfigMap
	err := s.Kube.Get(ctx, client.ObjectKey{Namespace: cell.SystemNamespace, Name: cell.ModelsStatus}, &cm)
	if apierrors.IsNotFound(err) {
		return v, "", nil
	}
	if err != nil {
		return v, "", err
	}
	if err := json.Unmarshal([]byte(cm.Data[cell.ModelsStatusKey]), &v); err != nil {
		return v, "", fmt.Errorf("ConfigMap %s: %w", cell.ModelsStatus, err)
	}
	return v, cm.Data[cell.ModelsVersionKey], nil
}

func (s *Server) setModels(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var req struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"baseUrl"`
		Model    string `json:"model"`
		// APIKey empty keeps the key set before, for the same provider and
		// address.
		APIKey string `json:"apiKey"`
		// TokensPerHour limits each agent and app; 0 means no limit.
		TokensPerHour int64 `json:"tokensPerHour"`
	}
	if err := readJSON(r, &req); err != nil {
		return err
	}
	req.BaseURL = strings.TrimSuffix(req.BaseURL, "/")
	if req.Provider == "" || req.Provider == cell.ProviderAnthropic {
		// The gateway adds the Anthropic API's /v1 itself.
		req.BaseURL = strings.TrimSuffix(req.BaseURL, "/v1")
	}
	limit := []byte(nil)
	if req.TokensPerHour != 0 {
		limit = []byte(strconv.FormatInt(req.TokensPerHour, 10))
	}
	p := cell.ReadModelProvider(map[string][]byte{"provider": []byte(req.Provider), "baseUrl": []byte(req.BaseURL), "model": []byte(req.Model), "apiKey": []byte(req.APIKey), "tokensPerHour": limit})
	if err := p.Check(); err != nil {
		return errorf(http.StatusBadRequest, "%v", err)
	}
	if p.Provider == cell.ProviderOpenAI && p.BaseURL == "" {
		return errorf(http.StatusBadRequest, "baseUrl: the address of the OpenAI-compatible API, such as https://api.openai.com/v1")
	}
	before, version, err := s.models(r.Context())
	if err != nil {
		return err
	}
	// A key stays only with its provider and address: a key that follows a
	// new address would go wherever an admin points it. The status may lag
	// behind the settings, so the key is kept only on top of the version the
	// status was made from.
	// No address is Anthropic's own.
	address := func(u string) string {
		if u == "" && p.Provider == cell.ProviderAnthropic {
			return anthropicURL
		}
		return u
	}
	keep := p.APIKey == "" && before.Provider == p.Provider && address(before.BaseURL) == address(p.BaseURL) && version != ""
	if p.APIKey == "" && p.Provider == cell.ProviderAnthropic && (!keep || !before.Configured) {
		return errorf(http.StatusBadRequest, "apiKey: the provider's key")
	}
	// A kept key was checked when it was set; a new one is checked now.
	if !keep {
		check := s.CheckModels
		if check == nil {
			check = checkModels
		}
		if err := check(r.Context(), p); err != nil {
			return errorf(http.StatusBadRequest, "model provider check failed: %v", err)
		}
	}
	data := map[string]any{"provider": []byte(p.Provider), "model": []byte(p.Model), "baseUrl": nil, "tokensPerHour": nil}
	if p.BaseURL != "" {
		data["baseUrl"] = []byte(p.BaseURL)
	}
	if limit != nil {
		data["tokensPerHour"] = limit
	}
	switch {
	case p.APIKey != "":
		data["apiKey"] = []byte(p.APIKey)
	case !keep:
		// Another provider's or address's key must not go to this one.
		data["apiKey"] = nil
	}
	body := map[string]any{"data": data}
	if keep {
		body["metadata"] = map[string]any{"resourceVersion": version}
	}
	patch, err := json.Marshal(body)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: cell.ModelSettings}}
	err = s.Kube.Patch(r.Context(), secret, client.RawPatch(types.MergePatchType, patch))
	if keep && (apierrors.IsConflict(err) || apierrors.IsNotFound(err)) {
		return errorf(http.StatusConflict, "the provider changed meanwhile: give the key again")
	}
	if apierrors.IsNotFound(err) {
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{}
		for k, v := range data {
			if b, ok := v.([]byte); ok {
				secret.Data[k] = b
			}
		}
		err = s.Kube.Create(r.Context(), secret)
	}
	if err != nil {
		return err
	}
	if keep {
		p.APIKey = "kept"
	}
	writeJSON(w, http.StatusOK, p.View())
	return nil
}

// modelsUsage tells the tokens each account used, from the gateway's
// metrics: they count since the gateway last started.
func (s *Server) modelsUsage(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	u := s.ModelsMetrics
	if u == "" {
		u = cell.ModelsMetricsURL
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return errorf(http.StatusServiceUnavailable, "the model gateway does not answer: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return errorf(http.StatusServiceUnavailable, "the model gateway answered %d", res.StatusCode)
	}
	accounts, err := tokenUsage(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("model gateway metrics: %w", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
	return nil
}

// AccountUsage is the tokens one account used.
type AccountUsage struct {
	Account      string `json:"account"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
}

var metricLabel = regexp.MustCompile(`(\w+)="((?:[^"\\]|\\.)*)"`)

// tokenUsage sums the gateway's token histogram per account, over models
// and routes, sorted by account.
func tokenUsage(in io.Reader) ([]AccountUsage, error) {
	const name = "agentgateway_gen_ai_client_token_usage_sum{"
	sums := map[string]*AccountUsage{}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), name)
		if !ok {
			continue
		}
		i := strings.LastIndex(line, "} ")
		if i < 0 {
			return nil, fmt.Errorf("unreadable line %q", sc.Text())
		}
		f := strings.Fields(line[i+2:])
		if len(f) == 0 {
			return nil, fmt.Errorf("unreadable line %q", sc.Text())
		}
		v, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			return nil, fmt.Errorf("unreadable line %q", sc.Text())
		}
		labels := map[string]string{}
		for _, m := range metricLabel.FindAllStringSubmatch(line[:i], -1) {
			labels[m[1]] = m[2]
		}
		// Requests without a known key are refused before they reach a model.
		account := labels["account"]
		if account == "" {
			continue
		}
		a := sums[account]
		if a == nil {
			a = &AccountUsage{Account: account}
			sums[account] = a
		}
		switch labels["gen_ai_token_type"] {
		case "input":
			a.InputTokens += int64(v)
		case "output":
			a.OutputTokens += int64(v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]AccountUsage, 0, len(sums))
	for _, a := range sums {
		out = append(out, *a)
	}
	slices.SortFunc(out, func(a, b AccountUsage) int { return strings.Compare(a.Account, b.Account) })
	return out, nil
}

// checkModels lists the provider's models with the key. Only the status
// goes back: the provider's answer could hold the key.
const anthropicURL = "https://api.anthropic.com"

func checkModels(ctx context.Context, p cell.ModelProvider) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	base := p.BaseURL
	if base == "" {
		base = anthropicURL
	}
	path := "/models"
	if p.Provider == cell.ProviderAnthropic {
		path = "/v1/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	switch {
	case p.Provider == cell.ProviderAnthropic:
		req.Header.Set("x-api-key", p.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case p.APIKey != "":
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s refused the key (%d)", req.URL.Host, res.StatusCode)
	case res.StatusCode/100 != 2:
		return fmt.Errorf("%s answered %d", req.URL.Host, res.StatusCode)
	}
	return nil
}
