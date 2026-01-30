package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

type GenerateResponse struct {
	Response string `json:"response"`
}

var submitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit a task to the agent",
	Run: func(cmd *cobra.Command, args []string) {
		url, _ := cmd.Flags().GetString("url")
		content, _ := cmd.Flags().GetString("content")
		jsonOutput, _ := cmd.Flags().GetBool("json")

		// If content is empty, try reading from stdin
		if content == "" {
			stat, _ := os.Stdin.Stat()
			if (stat.Mode() & os.ModeCharDevice) == 0 {
				bytesContent, _ := io.ReadAll(os.Stdin)
				content = string(bytesContent)
			}
		}

		if content == "" {
			fmt.Println("Error: No content provided via -c or stdin")
			cmd.Usage()
			os.Exit(1)
		}

		payload := map[string]string{
			"model":  "gravity",
			"prompt": content,
		}
		jsonPayload, _ := json.Marshal(payload)

		resp, err := http.Post(url+"/api/generate", "application/json", bytes.NewBuffer(jsonPayload))
		if err != nil {
			fmt.Printf("Error submitting task to %s: %v\n", url, err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			fmt.Printf("Failed to submit task. Status: %s\nResponse: %s\n", resp.Status, string(body))
			os.Exit(1)
		}

		if jsonOutput {
			fmt.Println(string(body))
		} else {
			// Pretty print default
			// Parse JSON stream (Ollama often returns stream of objects)
			// But our current implementation might return a single object or stream.
			// Let's assume single object with "response" field for now based on typical usage,
			// but robustly handle stream if needed.
			// Actually, `io.ReadAll` read everything. If it's a stream of JSONs, we can concatenate "response" fields.

			// Try unmarshalling as a single object first
			var singleResponse GenerateResponse
			if err := json.Unmarshal(body, &singleResponse); err == nil && singleResponse.Response != "" {
				fmt.Println(singleResponse.Response)
				return
			}

			// If that fails or response is empty, maybe it's newline delimited JSON?
			// Let's try to parse line by line
			lines := bytes.Split(body, []byte("\n"))
			var fullResponse string
			for _, line := range lines {
				if len(bytes.TrimSpace(line)) == 0 {
					continue
				}
				var part GenerateResponse
				if err := json.Unmarshal(line, &part); err == nil {
					fullResponse += part.Response
				}
			}
			if fullResponse != "" {
				fmt.Println(fullResponse)
			} else {
				// Fallback to raw body if parsing fails
				fmt.Println(string(body))
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(submitCmd)
	submitCmd.Flags().StringP("url", "u", "http://localhost:8080", "Agent URL")
	submitCmd.Flags().StringP("content", "c", "", "Task content")
	submitCmd.Flags().Bool("json", false, "Output raw JSON")
}
