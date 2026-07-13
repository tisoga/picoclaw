package doctor

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
)

func NewDoctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run system diagnostics",
		Long:  "Check system health, config, auth tokens, and network status.",
		Run: func(cmd *cobra.Command, args []string) {
			runDoctor()
		},
	}
	return cmd
}

func runDoctor() {
	fmt.Println("=== PicoClaw Doctor ===")
	fmt.Printf("Version: %s\n", config.FormatVersion())
	fmt.Println()

	fmt.Println("[Config]")
	path := internal.GetConfigPath()
	fmt.Printf("Config path: %s\n", path)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		fmt.Printf("❌ Failed to load config: %v\n", err)
	} else {
		fmt.Println("✅ Config loaded successfully")
		fmt.Printf("Workspace: %s\n", cfg.Agents.Defaults.Workspace)
		activeChannels := 0
		for name, ch := range cfg.Channels {
			if ch.Enabled {
				activeChannels++
				fmt.Printf("  - Active channel: %s\n", name)
			}
		}
		if activeChannels == 0 {
			fmt.Println("⚠️ No channels enabled")
		}
	}
	fmt.Println()

	fmt.Println("[Authentication]")
	store, err := auth.LoadStore()
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("⚠️ No auth store found (auth.json missing)")
		} else {
			fmt.Printf("❌ Failed to load auth store: %v\n", err)
		}
	} else {
		if len(store.Credentials) == 0 {
			fmt.Println("⚠️ No credentials found")
		}
		for name, cred := range store.Credentials {
			status := "✅ Valid"
			if cred.IsExpired() {
				status = "❌ Expired"
			} else if cred.NeedsRefresh() {
				status = "⚠️ Needs refresh"
			}
			fmt.Printf("  - %s (%s): %s\n", name, cred.AuthMethod, status)
			if !cred.ExpiresAt.IsZero() {
				fmt.Printf("    Expires: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04:05"))
			}
		}
	}
	fmt.Println()
	
	fmt.Println("Diagnosis complete.")
}
