package main

import (
	"encoding/json"
	"os"
)

type Config struct {
	Provider     string `json:"provider"`
	ModelName    string `json:"model_name"`
	APIKey       string `json:"api_key"`
	Instruction  string `json:"instruction"`
	MCPServerURL string `json:"mcp_server_url"`
}

func loadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
