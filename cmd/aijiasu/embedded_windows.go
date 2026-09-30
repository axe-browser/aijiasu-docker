//go:build windows

package main

import (
	"encoding/json"
	"io"
)

func embeddedMain(_ io.Reader, output io.Writer) {
	_ = json.NewEncoder(output).Encode(struct {
		Protocol string `json:"protocol"`
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
	}{
		Protocol: "aijiasu-stdio-v1",
		Error:    "Windows 平台暂不支持 embedded 模式；请使用普通 CLI。",
	})
}
