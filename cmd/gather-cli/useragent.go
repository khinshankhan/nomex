package main

import (
	"os"
	"time"

	"github.com/khinshankhan/nomex/platform/useragent"
)

func getUserAgent() string {
	name := os.Getenv("APPNAME")
	if name == "" {
		name = "nomex"
	}
	version := os.Getenv("APPVERSION")
	if version == "" {
		version = "1.0"
	}
	url := os.Getenv("APPURL")
	if url == "" {
		panic("APPURL environment variable is not set")
	}

	parsedBuildTime, err := time.Parse("2006.01.02.150405", BuildDate)
	if err != nil {
		panic(err)
	}

	return useragent.Build(useragent.AppMeta{
		Name:    name,
		Version: version,
		URL:     url,
		Commit:  CommitHash,
		Built:   parsedBuildTime.UTC(),
	})
}
