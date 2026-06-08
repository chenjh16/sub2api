package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAIGroupDefaultServiceTierToBody(t *testing.T) {
	group := &Group{
		Platform:                 PlatformOpenAI,
		OpenAIDefaultServiceTier: "fast",
	}

	body, applied, err := applyOpenAIGroupDefaultServiceTierToBody([]byte(`{"model":"gpt-5.4"}`), group)
	require.NoError(t, err)
	require.Equal(t, "priority", applied)
	require.Equal(t, "priority", gjson.GetBytes(body, "service_tier").String())

	body, applied, err = applyOpenAIGroupDefaultServiceTierToBody([]byte(`{"model":"gpt-5.4","service_tier":"flex"}`), group)
	require.NoError(t, err)
	require.Empty(t, applied)
	require.Equal(t, "flex", gjson.GetBytes(body, "service_tier").String())

	body, applied, err = applyOpenAIGroupDefaultServiceTierToBody([]byte(`{"model":"gpt-5.4","service_tier":""}`), group)
	require.NoError(t, err)
	require.Empty(t, applied)
	require.True(t, gjson.GetBytes(body, "service_tier").Exists(), "explicit empty service_tier should not be overwritten")
	require.Empty(t, gjson.GetBytes(body, "service_tier").String())
}

func TestApplyOpenAIGroupDefaultServiceTierRequiresOpenAIGroup(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4"}`)
	updated, applied, err := applyOpenAIGroupDefaultServiceTierToBody(body, &Group{
		Platform:                 PlatformAnthropic,
		OpenAIDefaultServiceTier: "priority",
	})
	require.NoError(t, err)
	require.Empty(t, applied)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIGroupDefaultServiceTierToWSResponseCreate(t *testing.T) {
	group := &Group{
		Platform:                 PlatformOpenAI,
		OpenAIDefaultServiceTier: "priority",
	}

	frame, applied, err := applyOpenAIGroupDefaultServiceTierToWSResponseCreate([]byte(`{"type":"response.create","model":"gpt-5.4"}`), group)
	require.NoError(t, err)
	require.Equal(t, "priority", applied)
	require.Equal(t, "priority", gjson.GetBytes(frame, "service_tier").String())

	frame, applied, err = applyOpenAIGroupDefaultServiceTierToWSResponseCreate([]byte(`{"type":"response.cancel","service_tier":"flex"}`), group)
	require.NoError(t, err)
	require.Empty(t, applied)
	require.Equal(t, "flex", gjson.GetBytes(frame, "service_tier").String())
}
