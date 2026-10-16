//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAntigravityAccountTest_MappedPromptsAndImageEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, model, prompt, wantPrompt string
		image                           bool
	}{
		{"localized Gemini alias", "gemini-4-argon", "", defaultTextTestPromptZH, false},
		{"custom Gemini alias", "gemini-4-argon", "  custom probe  ", "custom probe", false},
		{"localized Claude alias", "claude-sonnet-4-6", "", defaultTextTestPromptZH, false},
		{"default image alias", "gemini-3.1-flash-image", "", defaultGeminiImageTestPrompt, true},
		{"custom image alias", "gemini-3.1-flash-image", "draw a tiny robot", "draw a tiny robot", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}},
				Body: io.NopCloser(strings.NewReader("data: " +
					`{"response":{"candidates":[{"content":{"parts":[{"text":"probe ok"},{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}}]}}` + "\n\n")),
			}}}
			gateway := &AntigravityGatewayService{
				tokenProvider:  &AntigravityTokenProvider{},
				httpUpstream:   upstream,
				settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{}),
			}
			account := &Account{
				ID: 17, Platform: PlatformAntigravity, Type: AccountTypeUpstream,
				Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{
					"api_key": "synthetic-test-key", "project_id": "test-project",
					"model_mapping": map[string]any{"probe-alias": tc.model},
				},
			}
			svc := &AccountTestService{antigravityGatewayService: gateway}
			c, recorder := newTestContext()
			require.NoError(t, svc.testAntigravityAccountConnection(c, account, "probe-alias", tc.prompt, "zh-CN"))
			require.Len(t, upstream.requestBodies, 1)
			body := upstream.requestBodies[0]
			require.Equal(t, tc.model, gjson.GetBytes(body, "model").String())
			require.Contains(t, gjson.GetBytes(body, "request.contents").String(), tc.wantPrompt)
			if tc.image {
				require.Equal(t, `["TEXT","IMAGE"]`, gjson.GetBytes(body, "request.generationConfig.responseModalities").Raw)
				require.False(t, gjson.GetBytes(body, "request.generationConfig.maxOutputTokens").Exists())
			} else {
				require.Equal(t, int64(128), gjson.GetBytes(body, "request.generationConfig.maxOutputTokens").Int())
			}
			require.Contains(t, recorder.Body.String(), `"type":"image"`)
			require.Contains(t, recorder.Body.String(), "data:image/png;base64,iVBORw0KGgo=")
			require.Contains(t, recorder.Body.String(), `"type":"test_complete","success":true`)
		})
	}
}

func TestAntigravityConnectionTestModel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4-6", antigravityConnectionTestModel(""))
	require.Equal(t, "gemini-3.1-pro-preview", antigravityConnectionTestModel("gemini-3.1-pro-preview"))
}

func TestAntigravityBuildGeminiTestRequest_IncludesValidatedToolConfig(t *testing.T) {
	t.Parallel()

	svc := &AntigravityGatewayService{}
	raw, err := svc.buildGeminiTestRequestWithPrompt("proj-123", "gemini-3.8-flash-high", "")
	require.NoError(t, err)

	var v1Req map[string]any
	require.NoError(t, json.Unmarshal(raw, &v1Req))
	require.Equal(t, "proj-123", v1Req["project"])
	require.Equal(t, "gemini-3.8-flash-high", v1Req["model"])

	inner, ok := v1Req["request"].(map[string]any)
	require.True(t, ok)
	toolCfg, ok := inner["toolConfig"].(map[string]any)
	require.True(t, ok, "expected toolConfig to be present in Gemini test request")
	fcCfg, ok := toolCfg["functionCallingConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "VALIDATED", fcCfg["mode"])
}

func TestAntigravityBuildGeminiTestRequestWithPrompt_ImageModel(t *testing.T) {
	t.Parallel()

	svc := &AntigravityGatewayService{}
	raw, err := svc.buildGeminiTestRequestWithPrompt("proj-123", "gemini-3.1-flash-image", "a cute cat")
	require.NoError(t, err)

	var v1Req map[string]any
	require.NoError(t, json.Unmarshal(raw, &v1Req))
	inner, ok := v1Req["request"].(map[string]any)
	require.True(t, ok)

	genCfg, ok := inner["generationConfig"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []any{"TEXT", "IMAGE"}, genCfg["responseModalities"])

	contents, ok := inner["contents"].([]any)
	require.True(t, ok)
	require.Len(t, contents, 1)
	firstContent := contents[0].(map[string]any)
	parts := firstContent["parts"].([]any)
	require.Equal(t, "a cute cat", parts[0].(map[string]any)["text"])
}

func TestExtractContentFromSSEResponse_ExtractsTextAndInlineImages(t *testing.T) {
	t.Parallel()

	sse := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Here is your image\"},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"iVBORw0KGgo=\"}}]}}]}}\n\n"
	text, images := extractContentFromSSEResponse([]byte(sse))
	require.Equal(t, "Here is your image", text)
	require.Len(t, images, 1)
	require.Equal(t, "image/png", images[0].MimeType)
	require.Equal(t, "data:image/png;base64,iVBORw0KGgo=", images[0].ImageURL)
}

func TestAntigravityShouldUseGeminiTestBuilder_UsesMappedModel(t *testing.T) {
	t.Parallel()

	require.True(t, shouldUseAntigravityGeminiTestRequest("custom-alias", "gemini-2.5-flash"))
	require.True(t, shouldUseAntigravityGeminiTestRequest("gemini-3.1-pro", "gemini-pro-agent"))
	require.False(t, shouldUseAntigravityGeminiTestRequest("gemini-alias", "claude-sonnet-4-6"))
	require.False(t, shouldUseAntigravityGeminiTestRequest("claude-sonnet-4-6", "claude-sonnet-4-6"))
}
