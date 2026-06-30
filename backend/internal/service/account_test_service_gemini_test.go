//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCreateGeminiTestPayload_ImageModel(t *testing.T) {
	t.Parallel()

	payload := createGeminiTestPayload("gemini-2.5-flash-image", "draw a tiny robot")

	var parsed struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			ResponseModalities []string `json:"responseModalities"`
			ImageConfig        struct {
				AspectRatio string `json:"aspectRatio"`
			} `json:"imageConfig"`
		} `json:"generationConfig"`
	}

	require.NoError(t, json.Unmarshal(payload, &parsed))
	require.Len(t, parsed.Contents, 1)
	require.Len(t, parsed.Contents[0].Parts, 1)
	require.Equal(t, "draw a tiny robot", parsed.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, parsed.GenerationConfig.ResponseModalities)
	require.Equal(t, "1:1", parsed.GenerationConfig.ImageConfig.AspectRatio)
}

func TestCreateTextTestPayloadsUsePrompt(t *testing.T) {
	t.Parallel()

	claudePayload, err := createTestPayload("claude-sonnet-4-5", "你是什么模型？")
	require.NoError(t, err)
	claudeMessages := claudePayload["messages"].([]map[string]any)
	claudeContent := claudeMessages[0]["content"].([]map[string]any)
	require.Equal(t, "你是什么模型？", claudeContent[0]["text"])

	openAIPayload := createOpenAITestPayload("gpt-5.4", false, "你是什么模型？")
	openAIInput := openAIPayload["input"].([]map[string]any)
	openAIContent := openAIInput[0]["content"].([]map[string]any)
	require.Equal(t, "你是什么模型？", openAIContent[0]["text"])

	chatPayload := createOpenAIChatCompletionsTestPayload("gpt-5.4", "")
	chatMessages := chatPayload["messages"].([]map[string]any)
	require.Equal(t, defaultTextTestPrompt, chatMessages[0]["content"])

	geminiPayload := createGeminiTestPayload("gemini-2.5-pro", "你是什么模型？")
	var geminiParsed struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}
	require.NoError(t, json.Unmarshal(geminiPayload, &geminiParsed))
	require.Equal(t, "你是什么模型？", geminiParsed.Contents[0].Parts[0].Text)
}

func TestProcessGeminiStream_EmitsImageEvent(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	ctx, recorder := newTestContext()
	svc := &AccountTestService{}

	stream := strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"QUJD\"}}]}}]}\n\ndata: [DONE]\n\n")

	err := svc.processGeminiStream(ctx, stream)
	require.NoError(t, err)

	body := recorder.Body.String()
	require.Contains(t, body, "\"type\":\"content\"")
	require.Contains(t, body, "\"text\":\"ok\"")
	require.Contains(t, body, "\"type\":\"image\"")
	require.Contains(t, body, "\"image_url\":\"data:image/png;base64,QUJD\"")
	require.Contains(t, body, "\"mime_type\":\"image/png\"")
}
