package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration174AppendsGetChannelFailedRuleWithoutReplacingPolicy(t *testing.T) {
	sqlBytes, err := FS.ReadFile("174_add_openai_get_channel_failed_failover_rule.sql")
	require.NoError(t, err)

	sql := string(sqlBytes)
	require.Contains(t, sql, "openai_get_channel_failed_overloaded")
	require.Contains(t, sql, "policy_rules || jsonb_build_array(overload_rule)")
	require.Contains(t, sql, "jsonb_array_length(policy_rules) = 0")
	require.NotContains(t, strings.ToUpper(sql), "DELETE FROM SETTINGS")
}
