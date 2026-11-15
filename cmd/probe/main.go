package main

import (
	"fmt"

	"clipnest/internal/sensitive"
)

func main() {
	fmt.Println("front=", sensitive.FrontApp())
	fmt.Println("skip bitwarden=", sensitive.ShouldSkip(sensitive.Config{IgnoreSensitiveApps: true}, "com.bitwarden.desktop"))
}
