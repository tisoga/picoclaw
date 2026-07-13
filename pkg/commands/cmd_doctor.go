package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func doctorCommand() Definition {
	return Definition{
		Name:        "doctor",
		Description: "Run system diagnostics and show health status",
		Usage:       "/doctor",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			if rt == nil {
				return req.Reply("Command runtime unavailable")
			}

			var sb strings.Builder
			sb.WriteString("🩺 **PicoClaw Doctor**\n\n")
			sb.WriteString(fmt.Sprintf("**Version:** `%s`\n\n", config.FormatVersion()))

			sb.WriteString("**[Config]**\n")
			cfg := rt.Config
			if cfg != nil {
				sb.WriteString(fmt.Sprintf("- Workspace: `%s`\n", cfg.Agents.Defaults.Workspace))
				activeChannels := 0
				for name, ch := range cfg.Channels {
					if ch.Enabled {
						activeChannels++
						sb.WriteString(fmt.Sprintf("- Active channel: `%s`\n", name))
					}
				}
				if activeChannels == 0 {
					sb.WriteString("- ⚠️ No channels enabled\n")
				}
			} else {
				sb.WriteString("- ❌ Failed to load config\n")
			}
			sb.WriteString("\n")

			sb.WriteString("**[Authentication]**\n")
			store, err := auth.LoadStore()
			if err != nil {
				sb.WriteString(fmt.Sprintf("- ⚠️ Failed to load auth store: %v\n", err))
			} else if len(store.Credentials) == 0 {
				sb.WriteString("- ⚠️ No credentials found\n")
			} else {
				for name, cred := range store.Credentials {
					status := "✅ Valid"
					if cred.IsExpired() {
						status = "❌ Expired"
					} else if cred.NeedsRefresh() {
						status = "⚠️ Needs refresh"
					}
					sb.WriteString(fmt.Sprintf("- **%s** (`%s`): %s\n", name, cred.AuthMethod, status))
				}
			}

			return req.Reply(sb.String())
		},
	}
}
