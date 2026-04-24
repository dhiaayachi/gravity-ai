package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/dhiaayachi/gravity-ai/internal/state"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of the gravity agent",
	Run: func(cmd *cobra.Command, args []string) {
		url, _ := cmd.Flags().GetString("url")
		jsonOutput, _ := cmd.Flags().GetBool("json")

		// The endpoint is /api/agent/state
		resp, err := http.Get(url + "/api/agent/state")
		if err != nil {
			fmt.Printf("Error connecting to agent at %s: %v\n", url, err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Printf("Agent returned status: %s\n", resp.Status)
			os.Exit(1)
		}

		body, _ := io.ReadAll(resp.Body)

		if jsonOutput {
			fmt.Println(string(body))
		} else {
			var states []state.AgentState
			if err := json.Unmarshal(body, &states); err != nil {
				fmt.Printf("Failed to parse response: %v\nRaw: %s\n", err, string(body))
				return
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, "id\tRAFT STATE\tREPUTATION\tPROVIDER\tMODEL")
			for _, s := range states {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", s.ID, s.RaftState, s.Reputation, s.LLMProvider, s.LLMModel)
			}
			w.Flush()
		}
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
	statusCmd.Flags().StringP("url", "u", "http://localhost:8080", "Agent URL")
	statusCmd.Flags().Bool("json", false, "Output raw JSON")
}
