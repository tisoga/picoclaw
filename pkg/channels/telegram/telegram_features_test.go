package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func boolPtr(v bool) *bool { return &v }

func TestTelegramInlineRowsValidation(t *testing.T) {
	rows, err := telegramInlineRows([][]bus.InlineButton{{{Text: "Run", CallbackData: "run"}, {Text: "Docs", URL: "https://example.com"}}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "run", rows[0][0].CallbackData)

	_, err = telegramInlineRows([][]bus.InlineButton{{{Text: "bad", CallbackData: "x", URL: "https://example.com"}}})
	require.Error(t, err)
}

func TestTelegramAuthorizationAndTopicOverrides(t *testing.T) {
	base := channels.NewBaseChannel("telegram", nil, nil, []string{"100"})
	ch := &TelegramChannel{BaseChannel: base, tgCfg: &config.TelegramSettings{
		DMPolicy:    "allowlist",
		GroupPolicy: "allowlist",
		Groups: map[string]config.TelegramGroupSettings{
			"-1": {AllowFrom: config.FlexibleStringSlice{"200"}, RequireMention: boolPtr(true), Topics: map[string]config.TelegramTopicSettings{
				"42": {AllowFrom: config.FlexibleStringSlice{"300"}, RequireMention: boolPtr(false)},
			}},
		},
	}}

	require.True(t, ch.telegramSenderAllowed("100", 100, 0, true))
	require.False(t, ch.telegramSenderAllowed("200", 100, 0, true))
	require.True(t, ch.telegramSenderAllowed("200", -1, 0, false))
	require.False(t, ch.telegramSenderAllowed("200", -1, 42, false))
	require.True(t, ch.telegramSenderAllowed("300", -1, 42, false))
	require.Equal(t, false, *ch.telegramRequireMention(-1, 42))
}

func TestPollTargetCacheIsBounded(t *testing.T) {
	ch := &TelegramChannel{pollTargets: make(map[string]telegramPollTarget)}
	for i := 0; i < 140; i++ {
		ch.rememberPollTarget(string(rune(i+1)), "1", 0)
	}
	require.Len(t, ch.pollTargets, 128)
	require.Len(t, ch.pollTargetOrder, 128)
}
